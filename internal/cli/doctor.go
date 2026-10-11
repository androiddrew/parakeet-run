package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/androiddrew/parakeet-run/internal/assets"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
	"github.com/androiddrew/parakeet-run/internal/engine"
)

// doctorCommand runs _doctor in a subprocess with verbose native logging and
// checks the logged node placements, which prove where inference really runs.
func doctorCommand() *cobra.Command {
	var logPath string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Verify assets, native inference, and actual provider placement",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := resolve(cmd)
			if err != nil {
				return err
			}
			var log *os.File
			if logPath == "" {
				log, err = os.CreateTemp(l.Config.Server.TempDir, "parakeet-doctor-*.log")
			} else {
				log, err = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			}
			if err != nil {
				return err
			}
			defer log.Close()
			if logPath == "" {
				defer os.Remove(log.Name())
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			data, err := json.Marshal(l.Config)
			if err != nil {
				return err
			}
			child := exec.CommandContext(cmd.Context(), executable, "_doctor")
			child.Stdin = bytes.NewReader(data)
			child.Stderr = log
			var out bytes.Buffer
			child.Stdout = &out
			runErr := child.Run()
			report := map[string]any{}
			if runErr == nil {
				runErr = json.Unmarshal(out.Bytes(), &report)
			}
			if _, err := log.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if runErr == nil {
				placements, err := parsePlacements(log, l.Config.Runtime.Provider)
				report["session_placements"] = placements
				runErr = err
			}
			if runErr != nil {
				if info, err := log.Stat(); err == nil {
					_, _ = log.Seek(max(0, info.Size()-4096), io.SeekStart)
				}
				tail, _ := io.ReadAll(io.LimitReader(log, 4096))
				runErr = fmt.Errorf("doctor: %w; native log tail: %s", runErr, strings.TrimSpace(string(tail)))
				report["error"] = runErr.Error()
			}
			report["verified"] = runErr == nil
			if logPath != "" {
				report["log"] = logPath
			}
			return errors.Join(runErr, json.NewEncoder(cmd.OutOrStdout()).Encode(report))
		},
	}
	cmd.Flags().StringVar(&logPath, "log", "", "Retain complete native diagnostics in a new file")
	return cmd
}

// doctorWorkerCommand loads and warms one engine from the JSON config on stdin
// and reports versions and model details on stdout.
func doctorWorkerCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_doctor",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			var c config.Config
			d := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 1<<20))
			d.DisallowUnknownFields()
			if err := d.Decode(&c); err != nil {
				return err
			}
			if err := c.Validate(); err != nil {
				return err
			}
			if err := assets.VerifyAll(cmd.Context(), c.Model.Dir); err != nil {
				return err
			}
			if err := (audio.Decoder{Config: c.Audio}).Check(); err != nil {
				return err
			}
			c.Runtime.Verbose = true
			e, err := engine.Load(c)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, e.Close()) }()
			if err := e.Warm(cmd.Context()); err != nil {
				return err
			}
			deps := map[string]string{}
			if build, ok := debug.ReadBuildInfo(); ok {
				for _, dep := range build.Deps {
					deps[dep.Path] = dep.Version
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"version":         Version(),
				"go":              runtime.Version(),
				"dependencies":    deps,
				"runtime_library": c.Runtime.Library,
				"provider":        c.Runtime.Provider,
				"device_id":       c.Runtime.DeviceID,
				"initialization":  e.Initialization(),
				"model":           e.ModelInfo(),
				"model_revision":  assets.Revision,
				"assets":          assets.Manifest(),
				"ffmpeg":          c.Audio.FFmpeg,
			})
		},
	}
}

type placement struct {
	Nodes      int            `json:"nodes"`
	Operations map[string]int `json:"operations"`
	AllNodes   bool           `json:"all_nodes"`
}

var providerLine = regexp.MustCompile(`placed on \[(\w+)\]\. Number of nodes: (\d+)`)
var operationLine = regexp.MustCompile(`^\s+(\w+) \(`)

// v0.1.0 loads a metadata and an inference session for each graph, in order:
// preprocessor (CPU), encoder (selected provider), decoder (selected provider).
func parsePlacements(r io.Reader, requested string) ([]map[string]*placement, error) {
	var sessions []map[string]*placement
	var current map[string]*placement
	var provider *placement
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		i := strings.Index(line, "VerifyEachNodeIsAssignedToAnEp] ")
		if i < 0 {
			continue
		}
		line = line[i+len("VerifyEachNodeIsAssignedToAnEp] "):]
		if line == "Node placements" {
			current = map[string]*placement{}
			sessions = append(sessions, current)
			provider = nil
		} else if m := providerLine.FindStringSubmatch(line); m != nil {
			if current == nil || current[m[1]] != nil {
				return sessions, errors.New("invalid provider placement log")
			}
			n, err := strconv.Atoi(m[2])
			if err != nil {
				return sessions, err
			}
			provider = &placement{Nodes: n, Operations: map[string]int{}, AllNodes: strings.Contains(line, "All nodes placed on")}
			current[m[1]] = provider
		} else if m := operationLine.FindStringSubmatch(line); m != nil {
			if provider == nil {
				return sessions, errors.New("operation without provider")
			}
			provider.Operations[m[1]]++
		}
	}
	if err := scanner.Err(); err != nil {
		return sessions, err
	}
	if len(sessions) != 6 {
		return sessions, fmt.Errorf("expected six Parakeet session placements, got %d", len(sessions))
	}
	for i, session := range sessions {
		if i < 2 || requested == "cpu" {
			p := session["CPUExecutionProvider"]
			if len(session) != 1 || p == nil || p.Nodes <= 0 {
				return sessions, fmt.Errorf("session %d CPU placement not verified", i)
			}
		} else {
			p := session["CUDAExecutionProvider"]
			// ORT omits individual operations when the whole graph uses one
			// provider. That explicit all-nodes report also proves compute placement
			// for these known encoder/decoder graphs, unlike provider registration.
			if p == nil || p.Nodes <= 0 || (!(p.AllNodes && len(session) == 1) && p.Operations["MatMul"]+p.Operations["FusedMatMul"]+p.Operations["Gemm"]+p.Operations["Conv"]+p.Operations["LSTM"] == 0) {
				return sessions, fmt.Errorf("session %d CUDA compute placement not verified", i)
			}
		}
	}
	return sessions, nil
}
