package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androiddrew/parakeet-run/internal/assets"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
	"github.com/androiddrew/parakeet-run/internal/engine"
	"github.com/androiddrew/parakeet-run/internal/server"
)

const expected = "The little bird landed on the windowsill. Please open the blue door."

func fixture(t *testing.T) []float32 {
	t.Helper()
	f, err := os.Open("../testdata/speech.f32.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	data, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "f2c2589f09a7535fed93974a6f6ced3f6b4a101567e872f72edf3c2d7e311cbb" {
		t.Fatal("fixture checksum mismatch")
	}
	samples := make([]float32, len(data)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return samples
}

func TestFixture(t *testing.T) {
	if len(fixture(t)) != 60971 {
		t.Fatal("unexpected fixture length")
	}
}

func nativeConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Defaults()
	dir, ort, provider := os.Getenv("PARAKEET_RUN_TEST_ASSETS"), os.Getenv("PARAKEET_RUN_TEST_ORT"), os.Getenv("PARAKEET_RUN_TEST_PROVIDER")
	if dir == "" && ort == "" && provider == "" {
		t.Skip("set PARAKEET_RUN_TEST_ASSETS, PARAKEET_RUN_TEST_ORT and PARAKEET_RUN_TEST_PROVIDER for native tests")
	}
	if dir == "" || ort == "" || provider == "" {
		t.Fatal("partial native test configuration")
	}
	c.Model.Dir = dir
	c.Runtime.Library = ort
	c.Runtime.Provider = provider
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := assets.VerifyAll(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	return c
}

func transcribe(t *testing.T, e *engine.Engine, samples []float32) string {
	t.Helper()
	var text strings.Builder
	summary, err := e.Transcribe(context.Background(), samples, func(delta string) error { text.WriteString(delta); return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("audio %.2fs, windows %d, timings %+v", summary.AudioSeconds, summary.Windows, summary.Timings)
	return text.String()
}

func TestNative(t *testing.T) {
	c := nativeConfig(t)
	samples := fixture(t)
	// A missing asset must not poison subsequent loads of the same runtime.
	bad := c
	bad.Model.Dir = t.TempDir()
	if e, err := engine.Load(bad); err == nil {
		e.Close()
		t.Fatal("missing assets accepted")
	}
	e, err := engine.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for range 2 {
		if text := transcribe(t, e, samples); text != expected {
			t.Fatalf("transcript: %q", text)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Transcribe(ctx, samples, func(string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if text := transcribe(t, e, samples); text != expected {
		t.Fatal(text)
	}
	// HTTP uses actual encoded audio, real decoding, and the native engine.
	wav := wavBytes(t, samples)
	p := server.NewPool(1, 1)
	p.Add(e)
	httpServer := httptest.NewServer(server.New(c, p, audio.Decoder{Config: c.Audio}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer httpServer.Close()
	for _, format := range []string{"json", "text", "stream"} {
		t.Run(format, func(t *testing.T) {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := mw.CreateFormFile("file", "speech.wav")
			if err != nil {
				t.Fatal(err)
			}
			part.Write(wav)
			mw.WriteField("model", c.Model.ID)
			if format == "stream" {
				mw.WriteField("stream", "true")
			} else {
				mw.WriteField("response_format", format)
			}
			mw.Close()
			req, err := http.NewRequest("POST", httpServer.URL+"/v1/audio/transcriptions", &body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			resp, err := httpServer.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("%d %s", resp.StatusCode, data)
			}
			text := ""
			switch format {
			case "text":
				text = string(data)
			case "json":
				var v map[string]string
				if err := json.Unmarshal(data, &v); err != nil {
					t.Fatal(err)
				}
				text = v["text"]
			default:
				var deltas string
				for _, line := range strings.Split(string(data), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var v map[string]string
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
						t.Fatal(err)
					}
					if v["type"] == "transcript.text.delta" {
						deltas += v["delta"]
					}
					if v["type"] == "transcript.text.done" {
						text = v["text"]
					}
				}
				if deltas != text {
					t.Fatal("SSE deltas differ from completion")
				}
			}
			if text != expected {
				t.Fatalf("transcript: %q", text)
			}
		})
	}
	httpServer.Close()
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, err = engine.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if text := transcribe(t, e, samples); text != expected {
		t.Fatal(text)
	}
	var long []float32
	for range 12 {
		long = append(long, samples...)
		long = append(long, make([]float32, 16000/2)...)
	}
	text := transcribe(t, e, long)
	if strings.Count(text, "little bird") < 10 {
		t.Fatalf("chunked transcription lost speech: %q", text)
	}
	if os.Getenv("PARAKEET_RUN_TEST_LONG") == "1" {
		long = append(long, make([]float32, 301*16000-len(long))...)
		if text := transcribe(t, e, long); !strings.Contains(text, "little bird") {
			t.Fatal("long transcription lost speech")
		}
	}
	if text := transcribe(t, e, make([]float32, 16000)); text != "" {
		t.Logf("silence transcript: %q", text)
	}
}

func wavBytes(t *testing.T, samples []float32) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, v := range []any{[]byte("RIFF"), uint32(36 + len(samples)*4), []byte("WAVEfmt "), uint32(16), uint16(3), uint16(1), uint32(16000), uint32(64000), uint16(4), uint16(32), []byte("data"), uint32(len(samples) * 4), samples} {
		if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

// Optional fixture export supports SDK/container smoke tests without any model.
func TestExportFixture(t *testing.T) {
	dir := os.Getenv("PARAKEET_RUN_TEST_EXPORT")
	if dir == "" {
		t.Skip("fixture export not requested")
	}
	if err := os.WriteFile(filepath.Join(dir, "speech.wav"), wavBytes(t, fixture(t)), 0644); err != nil {
		t.Fatal(err)
	}
}
