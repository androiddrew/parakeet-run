// Package cli implements the parakeet-run commands: the transcription server
// and the local tools that share its configuration.
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

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/androiddrew/parakeet-run/internal/assets"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
	"github.com/androiddrew/parakeet-run/internal/engine"
)

// version is set at build time with -ldflags.
var version = "devel"

// Version is the build's version: the -ldflags value, else the module version.
func Version() string {
	if version != "devel" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// configKey is the flag annotation naming the setting a flag overrides.
const configKey = "config"

// bind marks flag name as the override for the setting at key.
func bind(f *pflag.FlagSet, name, key string) {
	if err := f.SetAnnotation(name, configKey, []string{key}); err != nil {
		panic(err)
	}
}

// NewCommand returns the root command with every subcommand.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "parakeet-run",
		Short:        "OpenAI-compatible speech transcription with Parakeet",
		Version:      Version(),
		SilenceUsage: true, SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	f := root.PersistentFlags()
	d := config.Defaults()
	f.String("config", "", "YAML configuration path (or "+config.Prefix+"CONFIG)")
	// Each flag below overrides one setting, and only when given; see resolve.
	f.String("model-dir", d.Model.Dir, "Directory containing the FP32 model bundle")
	bind(f, "model-dir", "model.dir")
	f.String("model-id", d.Model.ID, "Canonical served model ID")
	bind(f, "model-id", "model.id")
	f.String("model-aliases", "", "Comma-separated compatibility aliases")
	bind(f, "model-aliases", "model.aliases")
	f.Duration("window", d.Model.Window, "Inference window")
	bind(f, "window", "model.window")
	f.Duration("overlap", d.Model.Overlap, "Inference window overlap")
	bind(f, "overlap", "model.overlap")
	f.String("ort-library", d.Runtime.Library, "ONNX Runtime shared library path or loader name")
	bind(f, "ort-library", "runtime.library")
	f.String("provider", d.Runtime.Provider, "Execution provider: cpu or cuda")
	bind(f, "provider", "runtime.provider")
	f.Int("device-id", d.Runtime.DeviceID, "CUDA device index")
	bind(f, "device-id", "runtime.device_id")
	f.Int("threads", d.Runtime.Threads, "Intra-op threads per native session")
	bind(f, "threads", "runtime.threads")
	f.Bool("verbose", false, "Enable native ONNX Runtime placement logs")
	bind(f, "verbose", "runtime.verbose")
	f.String("ffmpeg", d.Audio.FFmpeg, "FFmpeg executable")
	bind(f, "ffmpeg", "audio.ffmpeg")
	f.Duration("max-audio-duration", d.Audio.MaxDuration, "Maximum decoded recording duration")
	bind(f, "max-audio-duration", "audio.max_duration")
	f.String("listen", d.Server.Listen, "HTTP listen address")
	bind(f, "listen", "server.listen")
	f.Int("replicas", d.Server.Replicas, "Number of independently loaded models")
	bind(f, "replicas", "server.replicas")
	f.Int("max-queue", d.Server.MaxQueue, "Additional request admission slots")
	bind(f, "max-queue", "server.max_queue")
	f.Bool("preload", d.Server.Preload, "Load and warm all replicas before listening")
	bind(f, "preload", "server.preload")
	f.Int64("max-upload-bytes", d.Server.MaxUploadBytes, "Maximum uploaded audio bytes")
	bind(f, "max-upload-bytes", "server.max_upload_bytes")
	f.Duration("request-timeout", d.Server.RequestTimeout, "Overall cooperative request deadline")
	bind(f, "request-timeout", "server.request_timeout")
	f.String("temp-dir", "", "Directory for temporary uploads (default: OS temporary directory)")
	bind(f, "temp-dir", "server.temp_dir")
	root.AddCommand(configCommand(), pullCommand(), transcribeCommand(), benchCommand(), serveCommand(),
		doctorCommand(), doctorWorkerCommand(), healthcheckCommand())
	return root
}

// resolve layers cmd's given flags over the defaults, config file and
// environment.
func resolve(cmd *cobra.Command) (config.Loaded, error) {
	path, _ := cmd.Flags().GetString("config")
	overrides := map[string]string{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if keys := f.Annotations[configKey]; len(keys) == 1 {
			overrides[keys[0]] = f.Value.String()
		}
	})
	return config.Load(path, overrides)
}

// logger returns the configured structured logger, writing to w.
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
	return &cobra.Command{
		Use:   "config",
		Short: "Print effective configuration with source comments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := resolve(cmd)
			if err != nil {
				return err
			}
			return l.Write(cmd.OutOrStdout())
		},
	}
}

func pullCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Download and verify the pinned FP32 assets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			progress := func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) }
			return assets.Pull(cmd.Context(), assets.Client(), l.Config.Model.Dir, progress)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "Destination directory (overrides --model-dir)")
	return cmd
}

// prepare verifies the assets, decodes the recording at path and loads an
// engine. The caller closes the engine.
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
	cmd := &cobra.Command{
		Use:   "transcribe FILE",
		Short: "Transcribe a local audio file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
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
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or streaming jsonl")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Create an output file exclusively (default: stdout)")
	return cmd
}

func benchCommand() *cobra.Command {
	var runs int
	cmd := &cobra.Command{
		Use:   "bench FILE",
		Short: "Measure cold and warm transcription performance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
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
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"provider":       c.Runtime.Provider,
				"initialization": e.Initialization(),
				"audio_seconds":  float64(len(samples)) / audio.SampleRate,
				"run_seconds":    durations,
			})
		},
	}
	cmd.Flags().IntVar(&runs, "runs", 3, "Number of sequential inference runs; first is cold")
	return cmd
}

// healthcheckCommand is the image's HEALTHCHECK: it checks /readyz on the
// configured listener without needing curl in the image.
func healthcheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_healthcheck",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			url := "http://" + net.JoinHostPort(host, port) + "/readyz"
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, url, nil)
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
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("readiness HTTP %d", resp.StatusCode)
			}
			return nil
		},
	}
}
