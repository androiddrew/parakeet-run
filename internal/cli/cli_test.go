package cli

import (
	"bytes"
	"strings"
	"testing"
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
