package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseNotesGate(t *testing.T) {
	script, err := filepath.Abs("release-notes.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, changelog string
		valid           bool
	}{
		{"missing", "## [Unreleased]\n\n- Pending.\n", false},
		{"empty", "## [1.2.3] - 2026-10-09\n\n## [1.2.2] - 2026-10-08\n- Old.\n", false},
		{"valid", "## [Unreleased]\n- Future.\n\n## [1.2.3] - 2026-10-09\n\n### Added\n- Ready.\n\n## [1.2.2] - 2026-10-08\n- Old.\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(tc.changelog), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", script, "v1.2.3")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("%v: %s", err, out)
			}
			if tc.valid && (!strings.Contains(string(out), "- Ready.") || strings.Contains(string(out), "Future") || strings.Contains(string(out), "Old")) {
				t.Fatal(string(out))
			}
		})
	}
}
