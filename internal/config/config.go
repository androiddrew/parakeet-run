// Package config implements defaults < YAML < environment < explicit flags.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Prefix starts every environment variable name, which is the prefix and the
// setting's YAML path in upper case, such as PARAKEET_RUN_SERVER_LISTEN.
const Prefix = "PARAKEET_RUN_"

// Config is every setting, in its YAML layout. APIKey is never read from YAML.
type Config struct {
	Runtime Runtime `yaml:"runtime"`
	Model   Model   `yaml:"model"`
	Audio   Audio   `yaml:"audio"`
	Server  Server  `yaml:"server"`
	Logging Logging `yaml:"logging"`
	APIKey  string  `yaml:"-" json:"-"`
}

// Runtime selects ONNX Runtime and its execution provider.
type Runtime struct {
	Library  string `yaml:"library"`
	Provider string `yaml:"provider"`
	DeviceID int    `yaml:"device_id"`
	Threads  int    `yaml:"threads"`
	Verbose  bool   `yaml:"verbose"`
}

// Model locates the model bundle and names it, and sets the inference windows.
type Model struct {
	Dir     string        `yaml:"dir"`
	ID      string        `yaml:"id"`
	Aliases []string      `yaml:"aliases"`
	Window  time.Duration `yaml:"window"`
	Overlap time.Duration `yaml:"overlap"`
}

// Audio bounds decoding with FFmpeg.
type Audio struct {
	FFmpeg        string        `yaml:"ffmpeg"`
	MaxDuration   time.Duration `yaml:"max_duration"`
	DecodeTimeout time.Duration `yaml:"decode_timeout"`
}

// Server bounds the HTTP server's capacity, uploads and timeouts.
type Server struct {
	Listen          string        `yaml:"listen"`
	Replicas        int           `yaml:"replicas"`
	MaxQueue        int           `yaml:"max_queue"`
	Preload         bool          `yaml:"preload"`
	MaxUploadBytes  int64         `yaml:"max_upload_bytes"`
	UploadTimeout   time.Duration `yaml:"upload_timeout"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	TempDir         string        `yaml:"temp_dir"`
	APIKeyFile      string        `yaml:"api_key_file"`
	Metrics         bool          `yaml:"metrics"`
}

// Logging selects the log format and level.
type Logging struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Defaults are the built-in settings.
func Defaults() Config {
	return Config{
		Runtime: Runtime{Library: "libonnxruntime.so", Provider: "cpu", Threads: 1},
		Model:   Model{Dir: "assets", ID: "parakeet-tdt-0.6b-v3", Aliases: []string{}, Window: 30 * time.Second, Overlap: 4 * time.Second},
		Audio:   Audio{FFmpeg: "ffmpeg", MaxDuration: 30 * time.Minute, DecodeTimeout: 2 * time.Minute},
		Server: Server{Listen: "127.0.0.1:8880", Replicas: 1, MaxQueue: 8, Preload: true,
			MaxUploadBytes: 25_000_000, UploadTimeout: time.Minute, RequestTimeout: 10 * time.Minute,
			WriteTimeout: 30 * time.Second, ShutdownTimeout: 2 * time.Minute, Metrics: true},
		Logging: Logging{Format: "json", Level: "info"},
	}
}

// Loaded is a resolved Config with the source of each setting that is not
// a default: the config file's path, an environment variable or "flag".
type Loaded struct {
	Config  Config
	Sources map[string]string
}

// fields calls fn with each leaf setting's dotted YAML path and value.
func fields(v reflect.Value, prefix string, fn func(string, reflect.Value) error) error {
	for i := range v.NumField() {
		name := v.Type().Field(i).Tag.Get("yaml")
		if name == "-" {
			continue
		}
		if prefix != "" {
			name = prefix + "." + name
		}
		f := v.Field(i)
		if f.Kind() == reflect.Struct {
			if err := fields(f, name, fn); err != nil {
				return err
			}
		} else if err := fn(name, f); err != nil {
			return err
		}
	}
	return nil
}

// set parses raw into v, by v's type.
func set(v reflect.Value, raw string) error {
	if v.Type() == reflect.TypeFor[time.Duration]() {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Slice:
		values := []string{}
		if raw != "" {
			for _, s := range strings.Split(raw, ",") {
				values = append(values, strings.TrimSpace(s))
			}
		}
		v.Set(reflect.ValueOf(values))
	default:
		return fmt.Errorf("unsupported configuration type %s", v.Type())
	}
	return nil
}

// Load reads configuration and the authentication secret. Relative paths remain
// relative to the process working directory, including paths from YAML.
func Load(path string, overrides map[string]string) (Loaded, error) {
	l := Loaded{Config: Defaults(), Sources: map[string]string{}}
	if path == "" {
		path = os.Getenv(Prefix + "CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return l, err
		}
		d := yaml.NewDecoder(bytes.NewReader(data))
		d.KnownFields(true)
		if err := d.Decode(&l.Config); err != nil {
			return l, fmt.Errorf("config: %w", err)
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return l, errors.New("config must contain exactly one YAML document")
		}
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return l, err
		}
		var mark func(*yaml.Node, string)
		mark = func(n *yaml.Node, prefix string) {
			if n.Kind == yaml.DocumentNode {
				for _, c := range n.Content {
					mark(c, prefix)
				}
				return
			}
			if n.Kind != yaml.MappingNode {
				l.Sources[prefix] = path
				return
			}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if prefix != "" {
					key = prefix + "." + key
				}
				mark(n.Content[i+1], key)
			}
		}
		mark(&node, "")
	}
	known := map[string]bool{}
	err := fields(reflect.ValueOf(&l.Config).Elem(), "", func(key string, v reflect.Value) error {
		known[key] = true
		env := Prefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		if raw, ok := os.LookupEnv(env); ok {
			if err := set(v, raw); err != nil {
				return fmt.Errorf("%s: %w", env, err)
			}
			l.Sources[key] = env
		}
		if raw, ok := overrides[key]; ok {
			if err := set(v, raw); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			l.Sources[key] = "flag"
		}
		return nil
	})
	if err != nil {
		return l, err
	}
	for key := range overrides {
		if !known[key] {
			return l, fmt.Errorf("unknown config override %q", key)
		}
	}
	if secret, ok := os.LookupEnv(Prefix + "API_KEY"); ok {
		l.Config.APIKey = strings.TrimSpace(secret)
		if l.Config.APIKey == "" {
			return l, errors.New("PARAKEET_RUN_API_KEY must not be empty when set")
		}
	} else if l.Config.Server.APIKeyFile != "" {
		f, err := os.Open(l.Config.Server.APIKeyFile)
		if err != nil {
			return l, fmt.Errorf("API key file: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(f, 65537))
		closeErr := f.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return l, err
		}
		if len(data) > 65536 {
			return l, errors.New("API key file exceeds 64 KiB")
		}
		l.Config.APIKey = strings.TrimSpace(string(data))
		if l.Config.APIKey == "" {
			return l, errors.New("API key file is empty")
		}
	}
	return l, l.Config.Validate()
}

// Validate reports every invalid setting.
func (c Config) Validate() error {
	var errs []error
	check := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}
	check(c.Runtime.Library != "", "runtime.library is required")
	check(c.Runtime.Provider == "cpu" || c.Runtime.Provider == "cuda", "runtime.provider must be cpu or cuda")
	check(c.Runtime.DeviceID >= 0 && c.Runtime.Threads > 0, "runtime.device_id must be nonnegative and threads positive")
	check(c.Model.Dir != "" && c.Model.ID != "", "model.dir and model.id are required")
	seen := map[string]bool{c.Model.ID: true}
	for _, alias := range c.Model.Aliases {
		check(alias != "" && !seen[alias], "model.aliases must be nonempty and unique, excluding model.id")
		seen[alias] = true
	}
	check(c.Model.Window >= 2*time.Second && c.Model.Window <= 300*time.Second, "model.window must be between 2s and 300s")
	check(c.Model.Overlap >= 80*time.Millisecond && c.Model.Overlap <= c.Model.Window/2, "model.overlap must be between 80ms and half the window")
	check(c.Audio.FFmpeg != "", "audio.ffmpeg is required")
	check(c.Audio.MaxDuration >= 20*time.Millisecond && c.Audio.MaxDuration <= 24*time.Hour, "audio.max_duration must be between 20ms and 24h")
	check(c.Audio.DecodeTimeout > 0, "audio.decode_timeout must be positive")
	_, _, err := net.SplitHostPort(c.Server.Listen)
	check(err == nil, "server.listen must be host:port")
	check(c.Server.Replicas > 0 && c.Server.Replicas <= 64, "server.replicas must be between 1 and 64")
	check(c.Server.MaxQueue >= 0 && c.Server.MaxQueue <= 1024, "server.max_queue must be between 0 and 1024")
	check(c.Server.MaxUploadBytes > 0 && c.Server.MaxUploadBytes <= 1<<40, "server.max_upload_bytes must be between 1 and 1 TiB")
	check(c.Server.UploadTimeout > 0 && c.Server.RequestTimeout > 0 && c.Server.WriteTimeout > 0 && c.Server.ShutdownTimeout > 0, "server timeouts must be positive")
	check(c.Logging.Format == "json" || c.Logging.Format == "text", "logging.format must be json or text")
	check(c.Logging.Level == "debug" || c.Logging.Level == "info" || c.Logging.Level == "warn" || c.Logging.Level == "error", "logging.level must be debug, info, warn, or error")
	return errors.Join(errs...)
}

// Write prints the settings as YAML, commenting each with its source. The API
// key is never written.
func (l Loaded) Write(w io.Writer) error {
	var node yaml.Node
	if err := node.Encode(l.Config); err != nil {
		return err
	}
	var annotate func(*yaml.Node, string)
	annotate = func(n *yaml.Node, prefix string) {
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if prefix != "" {
				key = prefix + "." + key
			}
			value := n.Content[i+1]
			if value.Kind == yaml.MappingNode {
				annotate(value, key)
			} else {
				source := l.Sources[key]
				if source == "" {
					source = "default"
				}
				n.Content[i].HeadComment = "source: " + source
			}
		}
	}
	annotate(&node, "")
	e := yaml.NewEncoder(w)
	e.SetIndent(2)
	return errors.Join(e.Encode(&node), e.Close())
}
