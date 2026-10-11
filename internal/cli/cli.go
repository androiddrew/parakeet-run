package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/androiddrew/parakeet-run/internal/assets"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
	"github.com/androiddrew/parakeet-run/internal/engine"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var version = "devel"

func Version() string {
	if version != "devel" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "parakeet-run", Short: "OpenAI-compatible speech transcription with Parakeet", Version: Version(), SilenceUsage: true, SilenceErrors: true}
	root.CompletionOptions.DisableDefaultCmd = true
	f := root.PersistentFlags()
	defaults := config.Defaults()
	f.String("config", "", "YAML configuration path (or PARAKEET_RUN_CONFIG)")
	f.String("model-dir", defaults.Model.Dir, "Directory containing the FP32 model bundle")
	f.String("model-id", defaults.Model.ID, "Canonical served model ID")
	f.String("model-aliases", "", "Comma-separated compatibility aliases")
	f.String("ort-library", defaults.Runtime.Library, "ONNX Runtime shared library path or loader name")
	f.String("provider", defaults.Runtime.Provider, "Execution provider: cpu or cuda")
	f.Int("device-id", defaults.Runtime.DeviceID, "CUDA device index")
	f.Int("threads", defaults.Runtime.Threads, "Intra-op threads per native session")
	f.Bool("verbose", false, "Enable native ONNX Runtime placement logs")
	f.String("ffmpeg", defaults.Audio.FFmpeg, "FFmpeg executable")
	f.Duration("max-audio-duration", defaults.Audio.MaxDuration, "Maximum decoded recording duration")
	f.Duration("window", defaults.Model.Window, "Inference window")
	f.Duration("overlap", defaults.Model.Overlap, "Inference window overlap")
	f.String("listen", defaults.Server.Listen, "HTTP listen address")
	f.Int("replicas", defaults.Server.Replicas, "Number of independently loaded models")
	f.Int("max-queue", defaults.Server.MaxQueue, "Additional request admission slots")
	f.Bool("preload", defaults.Server.Preload, "Load and warm all replicas before listening")
	f.Int64("max-upload-bytes", defaults.Server.MaxUploadBytes, "Maximum uploaded audio bytes")
	f.Duration("request-timeout", defaults.Server.RequestTimeout, "Overall cooperative request deadline")
	f.String("temp-dir", "", "Directory for temporary uploads (default: OS temporary directory)")
	root.AddCommand(configCommand(), pullCommand(), transcribeCommand(), benchCommand(), serveCommand(), doctorCommand(), doctorWorkerCommand(), healthcheckCommand())
	return root
}

var flagKeys = map[string]string{
	"model-dir": "model.dir", "model-id": "model.id", "model-aliases": "model.aliases",
	"ort-library": "runtime.library", "provider": "runtime.provider", "device-id": "runtime.device_id", "threads": "runtime.threads", "verbose": "runtime.verbose",
	"ffmpeg": "audio.ffmpeg", "max-audio-duration": "audio.max_duration", "window": "model.window", "overlap": "model.overlap",
	"listen": "server.listen", "replicas": "server.replicas", "max-queue": "server.max_queue", "preload": "server.preload",
	"max-upload-bytes": "server.max_upload_bytes", "request-timeout": "server.request_timeout", "temp-dir": "server.temp_dir",
}

func resolve(cmd *cobra.Command) (config.Loaded, error) {
	path, _ := cmd.Flags().GetString("config")
	overrides := map[string]string{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if key := flagKeys[f.Name]; key != "" {
			overrides[key] = f.Value.String()
		}
	})
	return config.Load(path, overrides)
}

func logger(c config.Config, w io.Writer) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.Logging.Level))
	options := &slog.HandlerOptions{Level: level}
	if c.Logging.Format == "text" {
		return slog.New(slog.NewTextHandler(w, options))
	}
	return slog.New(slog.NewJSONHandler(w, options))
}

func configCommand() *cobra.Command {
	return &cobra.Command{Use: "config", Short: "Print effective configuration with source comments", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := resolve(cmd)
		if err != nil {
			return err
		}
		return l.Write(cmd.OutOrStdout())
	}}
}

func pullCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{Use: "pull", Short: "Download and verify the pinned FP32 assets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := resolve(cmd)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("dir") {
			l.Config.Model.Dir = dir
		}
		if l.Config.Model.Dir == "" {
			return errors.New("model directory must not be empty")
		}
		return assets.Pull(cmd.Context(), assets.Client(), l.Config.Model.Dir, func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) })
	}}
	cmd.Flags().StringVar(&dir, "dir", "", "Destination directory (overrides --model-dir)")
	return cmd
}

func prepare(cmd *cobra.Command, path string) (config.Config, []float32, *engine.Engine, error) {
	l, err := resolve(cmd)
	if err != nil {
		return l.Config, nil, nil, err
	}
	c := l.Config
	if err := assets.VerifyAll(cmd.Context(), c.Model.Dir); err != nil {
		return c, nil, nil, fmt.Errorf("verify model assets (run pull): %w", err)
	}
	d := audio.Decoder{Config: c.Audio}
	samples, err := d.Decode(cmd.Context(), path)
	if err != nil {
		return c, nil, nil, err
	}
	e, err := engine.Load(c)
	return c, samples, e, err
}

func transcribeCommand() *cobra.Command {
	var format, output string
	cmd := &cobra.Command{Use: "transcribe FILE", Short: "Transcribe a local audio file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) (err error) {
		if format != "text" && format != "json" && format != "jsonl" {
			return errors.New("format must be text, json, or jsonl")
		}
		_, samples, e, err := prepare(cmd, args[0])
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, e.Close()) }()
		out := cmd.OutOrStdout()
		if output != "" {
			f, openErr := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if openErr != nil {
				return openErr
			}
			out = f
			defer func() {
				err = errors.Join(err, f.Close())
				if err != nil {
					os.Remove(output)
				}
			}()
		}
		var text strings.Builder
		summary, err := e.Transcribe(cmd.Context(), samples, func(delta string) error {
			text.WriteString(delta)
			if format == "jsonl" && delta != "" {
				return json.NewEncoder(out).Encode(map[string]string{"type": "transcript.text.delta", "delta": delta})
			}
			return nil
		})
		if err != nil {
			return err
		}
		switch format {
		case "json":
			return json.NewEncoder(out).Encode(map[string]any{"text": text.String(), "summary": summary})
		case "jsonl":
			return json.NewEncoder(out).Encode(map[string]string{"type": "transcript.text.done", "text": text.String()})
		default:
			_, err = fmt.Fprintln(out, text.String())
			return err
		}
	}}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or streaming jsonl")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Create an output file exclusively (default: stdout)")
	return cmd
}

func benchCommand() *cobra.Command {
	var runs int
	cmd := &cobra.Command{Use: "bench FILE", Short: "Measure cold and warm transcription performance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) (err error) {
		if runs < 1 || runs > 1000 {
			return errors.New("runs must be between 1 and 1000")
		}
		c, samples, e, err := prepare(cmd, args[0])
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, e.Close()) }()
		durations := make([]float64, 0, runs)
		for range runs {
			start := time.Now()
			_, err := e.Transcribe(cmd.Context(), samples, func(string) error { return nil })
			if err != nil {
				return err
			}
			durations = append(durations, time.Since(start).Seconds())
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"provider": c.Runtime.Provider, "initialization": e.Initialization(), "audio_seconds": float64(len(samples)) / audio.SampleRate, "run_seconds": durations})
	}}
	cmd.Flags().IntVar(&runs, "runs", 3, "Number of sequential inference runs; first is cold")
	return cmd
}

func healthcheckCommand() *cobra.Command {
	return &cobra.Command{Use: "_healthcheck", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := resolve(cmd)
		if err != nil {
			return err
		}
		host, port, err := net.SplitHostPort(l.Config.Server.Listen)
		if err != nil {
			return err
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		defer client.CloseIdleConnections()
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("readiness HTTP %d", resp.StatusCode)
		}
		return nil
	}}
}
