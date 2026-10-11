// Package server exposes a bounded, OpenAI-compatible transcription API.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	parakeet "github.com/androiddrew/go-parakeet"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/config"
)

// Decoder converts an uploaded file to 16 kHz mono samples. audio.Decoder is one.
type Decoder interface {
	Decode(context.Context, string) ([]float32, error)
}

// Server is the HTTP handler for the API, health, readiness and metrics.
type Server struct {
	config       config.Config
	pool         *Pool
	decoder      Decoder
	logger       *slog.Logger
	models       map[string]bool
	handler      http.Handler
	requests     *prometheus.CounterVec
	latency      *prometheus.HistogramVec
	audioSeconds prometheus.Counter
}

// New returns a server lending pool's engines to transcription requests. A nil
// logger uses slog.Default.
func New(c config.Config, pool *Pool, decoder Decoder, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{config: c, pool: pool, decoder: decoder, logger: logger, models: map[string]bool{c.Model.ID: true}}
	for _, alias := range c.Model.Aliases {
		s.models[alias] = true
	}
	s.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "parakeet_requests_total", Help: "Transcription requests by outcome."}, []string{"status"})
	s.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "parakeet_duration_seconds", Help: "Transcription stage durations.", Buckets: prometheus.ExponentialBuckets(0.01, 3, 12)}, []string{"stage"})
	s.audioSeconds = prometheus.NewCounter(prometheus.CounterOpts{Name: "parakeet_audio_seconds_total", Help: "Decoded audio seconds submitted for transcription."})
	registry := prometheus.NewRegistry()
	registry.MustRegister(s.requests, s.latency, s.audioSeconds, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	for _, stage := range []string{"admitted", "active", "loaded", "queued"} {
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "parakeet_" + stage, Help: "Current " + stage + " capacity."}, func() float64 {
			a, b, c := pool.Stats()
			switch stage {
			case "admitted":
				return float64(a)
			case "active":
				return float64(b)
			case "loaded":
				return float64(c)
			default:
				return float64(max(0, a-b))
			}
		}))
	}
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "parakeet_ready", Help: "Whether model capacity is ready."}, func() float64 {
		if pool.Ready() {
			return 1
		}
		return 0
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !pool.Ready() {
			writeJSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	if c.Server.Metrics {
		mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	}
	mux.HandleFunc("/v1/", s.api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &apiError{status: 404, Message: "Not found.", Type: "invalid_request_error", Code: "not_found"})
	})
	s.handler = mux
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if s.config.APIKey != "" {
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		want, got := sha256.Sum256([]byte(s.config.APIKey)), sha256.Sum256([]byte(provided))
		if !ok || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, &apiError{status: 401, Message: "Invalid API key.", Type: "authentication_error", Code: "invalid_api_key"})
			return
		}
	}
	method := ""
	switch r.URL.Path {
	case "/v1/models":
		method = http.MethodGet
	case "/v1/audio/transcriptions":
		method = http.MethodPost
	}
	if method == "" {
		writeError(w, &apiError{status: 404, Message: "Not found.", Type: "invalid_request_error", Code: "not_found"})
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, &apiError{status: 405, Message: "Method not allowed.", Type: "invalid_request_error", Code: "method_not_allowed"})
		return
	}
	if method == http.MethodGet {
		type model struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int    `json:"created"`
			OwnedBy string `json:"owned_by"`
		}
		data := []model{{s.config.Model.ID, "model", 0, "parakeet-run"}}
		for _, alias := range s.config.Model.Aliases {
			data = append(data, model{alias, "model", 0, "parakeet-run"})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
		return
	}
	s.transcribe(w, r)
}

// apiError is an OpenAI-style error body with its HTTP status.
type apiError struct {
	status  int
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

func (e *apiError) Error() string { return e.Message }
func invalid(message, param string) *apiError {
	return &apiError{status: 400, Message: message, Type: "invalid_request_error", Param: param, Code: "invalid_parameter"}
}
func tooLarge() *apiError {
	return &apiError{status: 413, Message: "Audio exceeds the configured upload or decoded-duration limit.", Type: "invalid_request_error", Param: "file", Code: "audio_too_large"}
}

// classify maps an error to the API error a client sees. Native and decoder
// details are kept out of it.
func classify(err error) *apiError {
	var api *apiError
	if errors.As(err, &api) {
		return api
	}
	switch {
	case errors.Is(err, ErrBusy):
		return &apiError{status: 429, Message: "Request capacity is full. Retry later.", Type: "rate_limit_error", Code: "queue_full"}
	case errors.Is(err, ErrUnavailable):
		return &apiError{status: 503, Message: "Model is unavailable.", Type: "server_error", Code: "model_unavailable"}
	case errors.Is(err, context.DeadlineExceeded):
		return &apiError{status: 503, Message: "Transcription timed out.", Type: "server_error", Code: "request_timeout"}
	case errors.Is(err, audio.ErrTooLong):
		return tooLarge()
	case errors.Is(err, audio.ErrInvalid), errors.Is(err, parakeet.ErrInvalidAudio):
		return invalid("The file could not be decoded as supported audio.", "file")
	default:
		return &apiError{status: 500, Message: "Transcription failed.", Type: "server_error", Code: "internal_error"}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err *apiError) {
	// An early rejection may leave an arbitrarily slow request body unread.
	// Do not let net/http drain that body before sending the error or returning.
	w.Header().Set("Connection", "close")
	rc := http.NewResponseController(w)
	_ = rc.EnableFullDuplex()
	_ = rc.SetReadDeadline(time.Now())
	if err.status == 429 {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, err.status, map[string]any{"error": err})
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), s.config.Server.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	status := 200
	var failure *apiError
	defer func() {
		s.requests.WithLabelValues(strconv.Itoa(status)).Inc()
		s.latency.WithLabelValues("total").Observe(time.Since(started).Seconds())
		attrs := []any{"status", status, "duration_seconds", time.Since(started).Seconds()}
		level := slog.LevelInfo
		if failure != nil {
			level = slog.LevelWarn
			if status >= 500 {
				level = slog.LevelError
			}
			// Only log the public validation description and bounded field name,
			// never form values, original filenames, or uploaded audio.
			param := failure.Param
			if name, ok := param.(string); ok && len(name) > 128 {
				param = name[:128] + "..."
			}
			attrs = append(attrs, "error", failure.Message, "error_type", failure.Type, "error_code", failure.Code, "error_param", param)
		}
		s.logger.Log(ctx, level, "transcription", attrs...)
	}()
	streamed := false
	rc := http.NewResponseController(w)
	writeDeadline := func() {
		deadline := time.Now().Add(s.config.Server.WriteTimeout)
		if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
			deadline = end
		}
		_ = rc.SetWriteDeadline(deadline)
	}
	emit := func(event any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		writeDeadline()
		if !streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			streamed = true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		return rc.Flush()
	}
	fail := func(err error) {
		api := classify(err)
		// net/http cancels the request context on a socket read deadline too.
		// That is an upload timeout, not necessarily a disconnected client.
		if errors.Is(r.Context().Err(), context.Canceled) && api.Code != "upload_timeout" {
			status = 499
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			api = classify(context.DeadlineExceeded)
		}
		status = api.status
		failure = api
		// Detailed decoder/native errors are operational diagnostics, not API responses.
		if status >= 500 {
			s.logger.Error("transcription failed", "error", err)
		}
		if streamed {
			// Use a fresh bounded write deadline for the terminal error even when
			// the inference deadline expired; never append a successful done event.
			_ = rc.SetWriteDeadline(time.Now().Add(s.config.Server.WriteTimeout))
			data, _ := json.Marshal(map[string]any{"error": api})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			_ = rc.Flush()
		} else {
			_ = rc.SetWriteDeadline(time.Now().Add(s.config.Server.WriteTimeout))
			writeError(w, api)
		}
	}
	if err := s.pool.Admit(); err != nil {
		fail(err)
		return
	}
	defer s.pool.ReleaseAdmission()
	if r.ContentLength > s.config.Server.MaxUploadBytes+64*1024 {
		fail(tooLarge())
		return
	}
	uploadDeadline := started.Add(s.config.Server.UploadTimeout)
	if end, _ := ctx.Deadline(); end.Before(uploadDeadline) {
		uploadDeadline = end
	}
	_ = rc.SetReadDeadline(uploadDeadline)
	// Closing the body also unblocks uploads on servers that cannot set deadlines.
	body := r.Body
	stopRead := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopRead()
	u, err := s.readUpload(w, r)
	stopRead()
	if err != nil {
		fail(err)
		return
	}
	_ = rc.SetReadDeadline(time.Time{})
	defer os.Remove(u.path)
	s.latency.WithLabelValues("upload").Observe(time.Since(started).Seconds())
	queued := time.Now()
	e, err := s.pool.Acquire(ctx)
	if err != nil {
		fail(err)
		return
	}
	defer s.pool.Release(e)
	s.latency.WithLabelValues("queue").Observe(time.Since(queued).Seconds())
	decodeStart := time.Now()
	samples, err := s.decoder.Decode(ctx, u.path)
	if err != nil {
		fail(err)
		return
	}
	s.latency.WithLabelValues("decode").Observe(time.Since(decodeStart).Seconds())
	s.audioSeconds.Add(float64(len(samples)) / audio.SampleRate)
	var text strings.Builder
	inferenceStart := time.Now()
	first := true
	summary, err := e.Transcribe(ctx, samples, func(delta string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		text.WriteString(delta)
		if delta == "" {
			return nil
		}
		if first {
			first = false
			s.latency.WithLabelValues("first_delta").Observe(time.Since(started).Seconds())
		}
		if u.stream {
			return emit(map[string]string{"type": "transcript.text.delta", "delta": delta})
		}
		return nil
	})
	elapsed := time.Since(inferenceStart).Seconds()
	s.latency.WithLabelValues("inference").Observe(elapsed)
	if err != nil {
		fail(err)
		return
	}
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	if summary.AudioSeconds > 0 {
		s.logger.Debug("inference", "audio_seconds", summary.AudioSeconds, "real_time_factor", elapsed/summary.AudioSeconds, "windows", summary.Windows)
	}
	if u.stream {
		if err := emit(map[string]string{"type": "transcript.text.done", "text": text.String()}); err != nil {
			status = 499
		}
		return
	}
	writeDeadline()
	if u.format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, text.String())
		return
	}
	writeJSON(w, 200, map[string]string{"text": text.String()})
}
