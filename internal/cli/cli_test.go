package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/androiddrew/parakeet-run/internal/config"
)

func TestExplicitFlagsAndConfig(t *testing.T) {
	t.Setenv("PARAKEET_RUN_RUNTIME_THREADS", "7")
	for _, args := range [][]string{{"config"}, {"config", "--threads", "2"}} {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		want := "threads: 7"
		if len(args) > 1 {
			want = "threads: 2"
		}
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

// TestFlagsNameSettings checks that every setting flag names a real setting, so
// a new flag can't be silently ignored by resolve.
func TestFlagsNameSettings(t *testing.T) {
	for _, env := range []string{"CONFIG", "API_KEY"} {
		t.Setenv(config.Prefix+env, "") // restored after the test
		os.Unsetenv(config.Prefix + env)
	}
	NewCommand().PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "config" {
			return
		}
		keys := f.Annotations[configKey]
		if len(keys) != 1 {
			t.Errorf("--%s names no setting", f.Name)
			return
		}
		if _, err := config.Load("", map[string]string{keys[0]: f.DefValue}); err != nil {
			t.Errorf("--%s: %v", f.Name, err)
		}
	})
}

func TestPlacementEvidence(t *testing.T) {
	const prefix = "[V:onnxruntime:, session_state.cc:1260 VerifyEachNodeIsAssignedToAnEp] "
	var log strings.Builder
	for i := range 6 {
		provider := "CPUExecutionProvider"
		if i >= 2 {
			provider = "CUDAExecutionProvider"
		}
		log.WriteString(prefix + "Node placements\n" + prefix + " All nodes placed on [" + provider + "]. Number of nodes: 23\n")
	}
	if _, err := parsePlacements(strings.NewReader(log.String()), "cuda"); err != nil {
		t.Fatal(err)
	}
	if _, err := parsePlacements(strings.NewReader("CUDAExecutionProvider registered"), "cuda"); err == nil {
		t.Fatal("registration treated as compute evidence")
	}
	if _, err := parsePlacements(strings.NewReader(log.String()), "cpu"); err == nil {
		t.Fatal("wrong provider accepted")
	}
	bad := strings.ReplaceAll(log.String(), "All nodes placed on [CUDAExecutionProvider]", "Some nodes placed on [CUDAExecutionProvider]")
	if _, err := parsePlacements(strings.NewReader(bad), "cuda"); err == nil {
		t.Fatal("missing compute placement accepted")
	}
}
