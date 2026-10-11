package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLayeringAndSecretRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("runtime:\n  threads: 2\nserver:\n  request_timeout: 3m\nmodel:\n  aliases: [compat]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Prefix+"RUNTIME_THREADS", "4")
	t.Setenv(Prefix+"API_KEY", "secret-do-not-print")
	l, err := Load(path, map[string]string{"runtime.threads": "6"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.Runtime.Threads != 6 || l.Config.Server.RequestTimeout != 3*time.Minute || l.Config.APIKey != "secret-do-not-print" {
		t.Fatalf("bad layered config: %+v", l.Config.Runtime)
	}
	var b bytes.Buffer
	if err := l.Write(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "secret-do-not-print") || !strings.Contains(b.String(), "source: flag") {
		t.Fatal(b.String())
	}
}

func TestStrictConfig(t *testing.T) {
	for _, input := range []string{"server:\n  api_key: secret\n", "server:\n  typo: true\n", "runtime: {provider: metal}\n", "{}\n---\n{}\n", "model: {window: 1s}\n", "model: {aliases: [same, same]}\n"} {
		t.Run(input, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p, nil); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestKeyFilePrecedence(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := Load("", map[string]string{"server.api_key_file": key})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.APIKey != "from-file" {
		t.Fatal("key file not loaded")
	}
	t.Setenv(Prefix+"API_KEY", "from-env")
	l, err = Load("", map[string]string{"server.api_key_file": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.APIKey != "from-env" {
		t.Fatal("environment did not take precedence")
	}
	t.Setenv(Prefix+"API_KEY", "")
	if _, err := Load("", nil); err == nil {
		t.Fatal("empty explicitly set key must fail")
	}
}

// TestDockerConfigs loads the images' configs, so they can't drift from the
// settings: an unknown or misspelled key fails here instead of in a container.
func TestDockerConfigs(t *testing.T) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, Prefix) {
			t.Setenv(name, "") // restored after the test
			os.Unsetenv(name)
		}
	}
	paths, err := filepath.Glob("../../docker/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	jetson, err := filepath.Glob("../../docker/jetson-*/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, jetson...)
	if len(paths) != 4 {
		t.Fatalf("expected four shipped Docker configurations, got %d", len(paths))
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			l, err := Load(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			c, provider := l.Config, "cuda"
			if filepath.Base(p) == "config.yaml" && filepath.Base(filepath.Dir(p)) == "docker" {
				provider = "cpu"
			}
			if c.Runtime.Provider != provider || c.Server.Listen != ":8880" || c.Model.Dir != "/var/lib/parakeet-run/assets" ||
				c.Runtime.Library != "/opt/onnxruntime/lib/libonnxruntime.so" {
				t.Errorf("provider %s, listen %s, model dir %s, library %s",
					c.Runtime.Provider, c.Server.Listen, c.Model.Dir, c.Runtime.Library)
			}
		})
	}
}
