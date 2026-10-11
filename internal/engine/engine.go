// Package engine owns model sessions, but deliberately never unloads ORT.
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	parakeet "github.com/androiddrew/go-parakeet"

	"github.com/androiddrew/parakeet-run/internal/config"
)

// Transcriber transcribes one request at a time. *Engine is one.
type Transcriber interface {
	Transcribe(context.Context, []float32, func(string) error) (parakeet.Summary, error)
	Close() error
}

// Engine is one loaded Parakeet model with the configured windowing.
type Engine struct {
	native   *parakeet.Engine
	chunking parakeet.ChunkOptions
}

// Load loads the model bundle from c.Model.Dir on the configured provider.
// With cuda it fails early when no NVIDIA device is visible.
func Load(c config.Config) (*Engine, error) {
	if c.Runtime.Provider == "cuda" && runtime.GOOS == "linux" {
		_, desktop := os.Stat("/dev/nvidiactl")
		_, jetson := os.Stat("/dev/nvmap")
		if desktop != nil && jetson != nil {
			return nil, fmt.Errorf("CUDA requires a visible NVIDIA device (/dev/nvidiactl or /dev/nvmap)")
		}
	}
	native, err := parakeet.New(parakeet.Config{
		ORTLibrary: c.Runtime.Library, Provider: parakeet.Provider(c.Runtime.Provider),
		DeviceID: c.Runtime.DeviceID, Threads: c.Runtime.Threads, Verbose: c.Runtime.Verbose,
		PreprocessorPath: filepath.Join(c.Model.Dir, "nemo128.onnx"),
		EncoderPath:      filepath.Join(c.Model.Dir, "encoder-model.onnx"),
		DecoderPath:      filepath.Join(c.Model.Dir, "decoder_joint-model.onnx"),
	})
	if err != nil {
		return nil, err
	}
	return &Engine{native: native, chunking: parakeet.ChunkOptions{Window: c.Model.Window, Overlap: c.Model.Overlap}}, nil
}

// Transcribe transcribes 16 kHz mono samples, calling yield with each window's
// finalized text as it is decoded.
func (e *Engine) Transcribe(ctx context.Context, samples []float32, yield func(string) error) (parakeet.Summary, error) {
	return e.native.TranscribeStream(ctx, parakeet.Request{Samples: samples, SampleRate: parakeet.SampleRate, Chunking: e.chunking}, func(chunk parakeet.ChunkResult) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return yield(chunk.Text)
	})
}

// Warm runs one second of silence through the model, so the first request
// doesn't pay for lazy native initialization.
func (e *Engine) Warm(ctx context.Context) error {
	_, err := e.Transcribe(ctx, make([]float32, parakeet.SampleRate), func(string) error { return nil })
	return err
}

// Initialization reports the native runtime and provider that were loaded.
func (e *Engine) Initialization() parakeet.Initialization { return e.native.Initialization() }

// ModelInfo describes the loaded model.
func (e *Engine) ModelInfo() parakeet.ModelInfo { return e.native.ModelInfo() }

// Close releases the model sessions. ONNX Runtime stays loaded.
func (e *Engine) Close() error { return e.native.Close() }
