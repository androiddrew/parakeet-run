package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	parakeet "github.com/androiddrew/go-parakeet"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
)

type fakeEngine struct {
	run    func(context.Context, func(string) error) error
	closed atomic.Bool
	called atomic.Int32
}

func (f *fakeEngine) Transcribe(ctx context.Context, _ []float32, yield func(string) error) (parakeet.Summary, error) {
	f.called.Add(1)
	if f.run != nil {
		return parakeet.Summary{}, f.run(ctx, yield)
	}
	for _, delta := range []string{"Hello", "", " world."} {
		if err := yield(delta); err != nil {
			return parakeet.Summary{}, err
		}
	}
	return parakeet.Summary{AudioSeconds: 1}, nil
}
func (f *fakeEngine) Close() error { f.closed.Store(true); return nil }

type fakeDecoder struct {
	err   error
	calls atomic.Int32
}

func (d *fakeDecoder) Decode(_ context.Context, path string) ([]float32, error) {
	d.calls.Add(1)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return make([]float32, 16000), d.err
}

func setup(t *testing.T, change func(*config.Config), e *fakeEngine) (*Server, *Pool, *fakeDecoder) {
	t.Helper()
	c := config.Defaults()
	c.Server.TempDir = t.TempDir()
	if change != nil {
		change(&c)
	}
	p := NewPool(c.Server.Replicas, c.Server.MaxQueue)
	if e != nil {
		p.Add(e)
	}
	d := &fakeDecoder{}
	s := New(c, p, d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, p, d
}

func request(t *testing.T, fields [][2]string, data []byte) *http.Request {
	t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	if data != nil {
		p, err := m.CreateFormFile("file", "../../client-name.wav")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range fields {
		if err := m.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &b)
	r.Header.Set("Content-Type", m.FormDataContentType())
	return r
}

func normalFields(extra ...[2]string) [][2]string {
	return append([][2]string{{"model", "parakeet-tdt-0.6b-v3"}}, extra...)
}

func TestResponseContracts(t *testing.T) {
	for _, format := range []string{"json", "text", "stream"} {
		t.Run(format, func(t *testing.T) {
			s, _, _ := setup(t, nil, &fakeEngine{})
			fields := normalFields([2]string{"response_format", format})
			if format == "stream" {
				fields = normalFields([2]string{"stream", "true"})
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(t, fields, []byte("audio")))
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if format == "stream" {
				if w.Header().Get("Content-Type") != "text/event-stream" {
					t.Fatal(w.Header())
				}
				var text, done string
				for _, line := range strings.Split(w.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event map[string]string
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
						t.Fatal(err)
					}
					if event["type"] == "transcript.text.delta" {
						text += event["delta"]
					} else if event["type"] == "transcript.text.done" {
						done = event["text"]
					}
				}
				if text != "Hello world." || done != text {
					t.Fatal(w.Body.String())
				}
			} else if format == "text" {
				if w.Body.String() != "Hello world." {
					t.Fatal(w.Body.String())
				}
			} else {
				var result map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["text"] != "Hello world." {
					t.Fatal(w.Body.String(), err)
				}
			}
			entries, _ := os.ReadDir(s.config.Server.TempDir)
			if len(entries) != 0 {
				t.Fatal("upload leaked")
			}
		})
	}
}

func TestValidationAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields [][2]string
		data   []byte
		status int
	}{
		{"missing file", normalFields(), nil, 400}, {"empty file", normalFields(), []byte{}, 400}, {"missing model", nil, []byte("a"), 400},
		{"unknown model", [][2]string{{"model", "whisper-1"}}, []byte("a"), 400},
		{"prompt", normalFields([2]string{"prompt", "hint"}), []byte("a"), 400},
		{"language", normalFields([2]string{"language", "en"}), []byte("a"), 400},
		{"temperature", normalFields([2]string{"temperature", "0"}), []byte("a"), 400},
		{"timestamps", normalFields([2]string{"timestamp_granularities[]", "word"}), []byte("a"), 400},
		{"duplicate", normalFields([2]string{"model", "another"}), []byte("a"), 400},
		{"srt", normalFields([2]string{"response_format", "srt"}), []byte("a"), 400},
		{"stream-text", normalFields([2]string{"stream", "true"}, [2]string{"response_format", "text"}), []byte("a"), 400},
		{"oversize", normalFields(), bytes.Repeat([]byte("a"), 65), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &fakeEngine{}
			s, _, _ := setup(t, func(c *config.Config) { c.Server.MaxUploadBytes = 64 }, e)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(t, tc.fields, tc.data))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if e.called.Load() != 0 {
				t.Fatal("validation reached inference")
			}
			entries, _ := os.ReadDir(s.config.Server.TempDir)
			if len(entries) != 0 {
				t.Fatal("upload leaked")
			}
		})
	}
}

func TestAuthModelsHealthAndMetrics(t *testing.T) {
	s, _, _ := setup(t, func(c *config.Config) { c.APIKey = "secret"; c.Model.Aliases = []string{"compat"} }, &fakeEngine{})
	for _, tc := range []struct {
		path, method, key string
		status            int
	}{
		{"/v1/models", "GET", "", 401}, {"/v1/models", "GET", "wrong", 401}, {"/v1/models", "GET", "secret", 200},
		{"/v1/models", "POST", "secret", 405}, {"/v1/unknown", "GET", "secret", 404},
		{"/healthz", "GET", "", 200}, {"/readyz", "GET", "", 200}, {"/metrics", "GET", "", 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.key != "" {
			r.Header.Set("Authorization", "Bearer "+tc.key)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if tc.path == "/v1/models" && tc.status == 200 && !strings.Contains(w.Body.String(), "compat") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestStreamingFailure(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			e := &fakeEngine{run: func(ctx context.Context, yield func(string) error) error {
				if after {
					if err := yield("partial"); err != nil {
						return err
					}
				}
				return errors.New("private native path /secret")
			}}
			s, _, _ := setup(t, nil, e)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(t, normalFields([2]string{"stream", "true"}), []byte("a")))
			if strings.Contains(w.Body.String(), "/secret") || strings.Contains(w.Body.String(), "transcript.text.done") || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatal(w.Body.String())
			}
			if (!after && w.Code != 500) || (after && w.Code != 200) {
				t.Fatal(w.Code)
			}
		})
	}
}

func TestTimeoutRetainsNativeLease(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := &fakeEngine{run: func(ctx context.Context, yield func(string) error) error { close(started); <-release; return ctx.Err() }}
	s, p, _ := setup(t, func(c *config.Config) { c.Server.MaxQueue = 0; c.Server.RequestTimeout = 20 * time.Millisecond }, e)
	w := httptest.NewRecorder()
	go func() { defer close(done); s.ServeHTTP(w, request(t, normalFields(), []byte("a"))) }()
	<-started
	time.Sleep(40 * time.Millisecond)
	if err := p.Admit(); !errors.Is(err, ErrBusy) {
		t.Fatalf("lease released before native return: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if e.closed.Load() {
		t.Fatal("closed a busy native session")
	}
	close(release)
	<-done
	if w.Code != 503 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestAdmissionReadinessAndCancelledWait(t *testing.T) {
	s, p, d := setup(t, func(c *config.Config) { c.Server.MaxQueue = 0 }, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if err := p.Admit(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request(t, normalFields(), []byte("a")))
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Body.String())
	}
	if d.calls.Load() != 0 {
		t.Fatal("overload decoded audio")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.ReleaseAdmission()
	p.Fail()
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request(t, normalFields(), []byte("a")))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestDecodeErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{audio.ErrInvalid, 400}, {audio.ErrTooLong, 413}, {context.DeadlineExceeded, 503}} {
		s, _, d := setup(t, nil, &fakeEngine{})
		d.err = tc.err
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(t, normalFields(), []byte("a")))
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestSlowUploadDeadline(t *testing.T) {
	s, _, d := setup(t, func(c *config.Config) { c.Server.UploadTimeout = 50 * time.Millisecond }, &fakeEngine{})
	ts := httptest.NewServer(s)
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	// Deliberately stop halfway through the first multipart boundary.
	_, err = fmt.Fprint(conn, "POST /v1/audio/transcriptions HTTP/1.1\r\nHost: localhost\r\nContent-Type: multipart/form-data; boundary=slow\r\nContent-Length: 1024\r\n\r\n--sl")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 408 {
		t.Fatal(response.StatusCode)
	}
	if d.calls.Load() != 0 {
		t.Fatal("incomplete upload was decoded")
	}
}

func TestSlowSSEClientReleasesWorker(t *testing.T) {
	started := make(chan struct{})
	e := &fakeEngine{run: func(ctx context.Context, yield func(string) error) error {
		close(started)
		return yield(strings.Repeat("x", 8<<20))
	}}
	s, p, _ := setup(t, func(c *config.Config) { c.Server.WriteTimeout = 50 * time.Millisecond; c.Server.MaxQueue = 0 }, e)
	ts := httptest.NewServer(s)
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	r := request(t, normalFields([2]string{"stream", "true"}), []byte("audio"))
	if err := r.Write(conn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("inference did not start")
	}
	// Never read from the socket: the server must terminate its bounded write.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, b, _ := p.Stats()
		if a == 0 && b == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("slow SSE client retained a model worker")
}

func TestDisconnectCancelsInference(t *testing.T) {
	returned := make(chan struct{})
	e := &fakeEngine{run: func(ctx context.Context, yield func(string) error) error {
		defer close(returned)
		if err := yield("first"); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	s, _, _ := setup(t, nil, e)
	ts := httptest.NewServer(s)
	defer ts.Close()
	r := request(t, normalFields([2]string{"stream", "true"}), []byte("audio"))
	r.URL.Scheme = "http"
	r.URL.Host = ts.Listener.Addr().String()
	r.RequestURI = ""
	response, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel inference")
	}
}

func TestEarlyRejectionDoesNotDrainSlowBody(t *testing.T) {
	for _, code := range []int{401, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s, p, _ := setup(t, func(c *config.Config) {
				c.Server.MaxQueue = 0
				if code == 401 {
					c.APIKey = "required"
				}
			}, &fakeEngine{})
			if code == 429 {
				if err := p.Admit(); err != nil {
					t.Fatal(err)
				}
				defer p.ReleaseAdmission()
			}
			ts := httptest.NewServer(s)
			defer ts.Close()
			conn, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = fmt.Fprint(conn, "POST /v1/audio/transcriptions HTTP/1.1\r\nHost: localhost\r\nContent-Type: multipart/form-data; boundary=slow\r\nContent-Length: 1024\r\n\r\n")
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != code {
				t.Fatal(resp.StatusCode)
			}
		})
	}
}

func TestMultipartEpilogueRetainsUploadDeadline(t *testing.T) {
	s, _, d := setup(t, func(c *config.Config) { c.Server.UploadTimeout = 50 * time.Millisecond }, &fakeEngine{})
	ts := httptest.NewServer(s)
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	r := request(t, normalFields(), []byte("audio"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /v1/audio/transcriptions HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s", r.Header.Get("Content-Type"), len(body)+1024, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 408 {
		t.Fatal(resp.StatusCode)
	}
	if d.calls.Load() != 0 {
		t.Fatal("inference admitted before the HTTP body finished")
	}
}

func TestModelValidationDiagnostics(t *testing.T) {
	for _, model := range []string{"", "private-wrong-model"} {
		t.Run(model, func(t *testing.T) {
			s, _, _ := setup(t, func(c *config.Config) { c.Model.ID = "local-parakeet" }, &fakeEngine{})
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(t, [][2]string{{"model", model}}, []byte("private audio contents")))
			if w.Code != 400 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			var response struct {
				Error apiError `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Param != "model" || !strings.Contains(response.Error.Message, `"local-parakeet"`) {
				t.Fatalf("model error is not actionable: %+v", response.Error)
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != "WARN" || record["status"] != float64(400) || record["error_param"] != "model" || record["error_code"] != "invalid_parameter" || record["error"] != response.Error.Message {
				t.Fatal(logs.String())
			}
			for _, private := range []string{"private-wrong-model", "private audio contents", "client-name.wav"} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("request content leaked into logs: %s", private)
				}
			}
		})
	}
}

func TestValidationLogBoundsFieldNames(t *testing.T) {
	s, _, _ := setup(t, nil, &fakeEngine{})
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(t, normalFields([2]string{strings.Repeat("x", 256), "private-field-value"}), []byte("audio")))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	param, ok := record["error_param"].(string)
	if !ok || len(param) > 131 || strings.Contains(logs.String(), "private-field-value") {
		t.Fatal(logs.String())
	}
}

func TestOpenWebUIChunkedMultipart(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		aliases     []string
		status      int
	}{
		{"canonical", "parakeet-tdt-0.6b-v3", nil, 200},
		{"unconfigured-whisper", "whisper-1", nil, 400},
		{"explicit-alias", "whisper-1", []string{"whisper-1"}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &fakeEngine{}
			s, _, _ := setup(t, func(c *config.Config) { c.Model.Aliases = tc.aliases }, e)
			chunked := make(chan bool, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chunked <- r.ContentLength == -1 && len(r.TransferEncoding) == 1 && r.TransferEncoding[0] == "chunked"
				s.ServeHTTP(w, r)
			}))
			defer ts.Close()
			pr, pw := io.Pipe()
			defer pr.Close()
			mw := multipart.NewWriter(pw)
			written := make(chan error, 1)
			go func() {
				write := func() error {
					// Match aiohttp.FormData: text model first, then an async file
					// payload of unknown length with no special file media type.
					part, err := mw.CreatePart(textproto.MIMEHeader{
						"Content-Disposition": {`form-data; name="model"`},
						"Content-Type":        {"text/plain; charset=utf-8"},
					})
					if err != nil {
						return err
					}
					if _, err := io.WriteString(part, tc.model); err != nil {
						return err
					}
					part, err = mw.CreatePart(textproto.MIMEHeader{
						"Content-Disposition": {`form-data; name="file"; filename="recording.wav"`},
						"Content-Type":        {"application/octet-stream"},
					})
					if err != nil {
						return err
					}
					for _, fragment := range []string{"first audio frame", "second audio frame"} {
						if _, err := io.WriteString(part, fragment); err != nil {
							return err
						}
					}
					return mw.Close()
				}
				err := write()
				_ = pw.CloseWithError(err)
				written <- err
			}()
			r, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/audio/transcriptions", pr)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Content-Type", mw.FormDataContentType())
			resp, err := ts.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("%d: %s", resp.StatusCode, data)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if !<-chunked {
				t.Fatal("test did not exercise an unknown-length chunked upload")
			}
			if tc.status == 200 {
				if !strings.Contains(string(data), `"text":"Hello world."`) || e.called.Load() != 1 {
					t.Fatalf("%s; inference calls %d", data, e.called.Load())
				}
			} else if e.called.Load() != 0 || !strings.Contains(string(data), "parakeet-tdt-0.6b-v3") {
				t.Fatalf("unconfigured model was not rejected helpfully: %s", data)
			}
		})
	}
}
