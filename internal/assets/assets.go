// Package assets provisions the pinned FP32 model bundle independently of ORT.
package assets

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const Revision = "8f23f0c03c8761650bdb5b40aaf3e40d2c15f1ce"
const BaseURL = "https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx/resolve/" + Revision + "/"

//go:embed manifest.json
var manifest []byte

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Manifest() []File {
	var files []File
	if err := json.Unmarshal(manifest, &files); err != nil {
		panic(err)
	}
	return files
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func Verify(ctx context.Context, dir string, file File) error {
	f, err := os.Open(filepath.Join(dir, file.Name))
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return err
	}
	if !s.Mode().IsRegular() || s.Size() != file.Size {
		return fmt.Errorf("%s: expected regular file of %d bytes", file.Name, file.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("%s: SHA-256 mismatch", file.Name)
	}
	return nil
}

func VerifyAll(ctx context.Context, dir string) error {
	for _, f := range Manifest() {
		if err := Verify(ctx, dir, f); err != nil {
			return err
		}
	}
	return nil
}

// Pull verifies existing files, then downloads only missing or invalid assets.
// A failed transfer never replaces an existing destination.
func Pull(ctx context.Context, client *http.Client, dir string, progress func(string)) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, file := range Manifest() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := Verify(ctx, dir, file); err == nil {
			if progress != nil {
				progress("verified " + file.Name)
			}
			continue
		}
		if progress != nil {
			progress("downloading " + file.Name)
		}
		if err := Download(ctx, client, BaseURL+file.Name, dir, file); err != nil {
			return fmt.Errorf("%s: %w", file.Name, err)
		}
	}
	return nil
}

func Download(ctx context.Context, client *http.Client, url, dir string, file File) (err error) {
	if filepath.Base(file.Name) != file.Name || file.Name == "." || file.Size < 0 {
		return errors.New("invalid asset manifest entry")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, file.Size+1))
	if err != nil {
		return err
	}
	if n != file.Size {
		return fmt.Errorf("expected %d bytes, received %d", file.Size, n)
	}
	if hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		return errors.New("download SHA-256 mismatch")
	}
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, file.Name))
}
