package assets

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadIntegrityAndAtomicity(t *testing.T) {
	good := "verified model bytes"
	file := File{Name: "model.onnx", Size: int64(len(good)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(good)))}
	for _, body := range []string{good, "truncated", good + "extra", "wrong checksum bytes"} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, file.Name)
			if err := os.WriteFile(dest, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			err := Download(context.Background(), srv.Client(), srv.URL, dir, file)
			if (err == nil) != (body == good) {
				t.Fatalf("unexpected error: %v", err)
			}
			data, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			want := "old"
			if body == good {
				want = good
				if err := Verify(context.Background(), dir, file); err != nil {
					t.Fatal(err)
				}
			}
			if string(data) != want {
				t.Fatal("destination was corrupted")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("temporary download leaked")
			}
		})
	}
}

func TestCancelledDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Download(ctx, http.DefaultClient, "http://127.0.0.1/", t.TempDir(), File{Name: "model", Size: 4})
	if err == nil {
		t.Fatal("cancelled download succeeded")
	}
}

func TestClientHasNoOverallTimeout(t *testing.T) {
	// A total timeout would fail the 2.4 GB encoder download on slow links.
	if c := Client(); c.Timeout != 0 {
		t.Fatalf("client timeout %v", c.Timeout)
	}
}

func TestDownloadCancelledMidTransfer(t *testing.T) {
	file := File{Name: "model.onnx", Size: 1 << 20, SHA256: "unused"}
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(file.Size))
		w.Write(make([]byte, 1024))
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- Download(ctx, Client(), srv.URL, dir, file) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled download succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not stop the download")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("temporary download leaked")
	}
}
