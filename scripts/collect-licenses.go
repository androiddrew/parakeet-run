//go:build ignore

// Collect license/notice files from the pinned module graph for binary bundles.
// Run after go mod download: go run scripts/collect-licenses.go OUTPUT_DIRECTORY
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := collect(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func collect() error {
	if len(os.Args) != 2 {
		return errors.New("usage: collect-licenses.go OUTPUT_DIRECTORY")
	}
	out := os.Args[1]
	data, err := exec.Command("go", "list", "-deps", "-json", "./cmd/parakeet-run").Output()
	if err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	copyFile := func(src, dst string) error {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0644)
	}
	seen := map[string]bool{}
	for {
		var pkg struct {
			Module *struct {
				Path, Version, Dir string
				Main               bool
			}
		}
		if err := d.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		m := pkg.Module
		if m == nil || m.Main || seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		if m.Dir == "" {
			return fmt.Errorf("module %s not downloaded; run go mod download", m.Path)
		}
		dest := filepath.Join(out, strings.ReplaceAll(m.Path, "/", "_")+"@"+m.Version)
		found := false
		for _, pattern := range []string{"LICENSE*", "NOTICE*", "COPYING*", "COPYRIGHT*"} {
			paths, err := filepath.Glob(filepath.Join(m.Dir, pattern))
			if err != nil {
				return err
			}
			for _, path := range paths {
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					continue
				}
				if err := copyFile(path, filepath.Join(dest, filepath.Base(path))); err != nil {
					return err
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("no license found for %s", m.Path)
		}
		if m.Path == "github.com/androiddrew/go-parakeet" {
			if err := copyFile(filepath.Join(m.Dir, "third_party/licenses/onnx-asr.txt"), filepath.Join(dest, "onnx-asr.txt")); err != nil {
				return err
			}
		}
	}
	return copyFile(filepath.Join(runtime.GOROOT(), "LICENSE"), filepath.Join(out, "go", "LICENSE"))
}
