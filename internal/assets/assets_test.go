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
