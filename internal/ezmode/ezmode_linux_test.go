package ezmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit quotes what systemd would otherwise expand or split.
func TestEzUnit(t *testing.T) {
	unit := unitText(Spec{Name: "inaba"}, "/home/a b/bin/inaba", []string{"connect", "--data", "/d/100%/$HOME/x.db"}, `/usr/bin:/odd"%dir`, "/home/a b/.config/inaba/host.env")
	for _, want := range []string{
		`ExecStart="/home/a b/bin/inaba" "connect" "--data" "/d/100%%/$$HOME/x.db"`,
		`Environment="PATH=/usr/bin:/odd\"%%dir"`,
		`EnvironmentFile=-/home/a b/.config/inaba/host.env`,
	} {
		if !strings.Contains(unit, want+"\n") {
			t.Errorf("no line %s in\n%s", want, unit)
		}
	}
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze to check the unit with")
	}
	dir := t.TempDir()
	text := unitText(Spec{Name: "inaba"}, "/bin/true", []string{"connect"}, "/usr/bin:/bin", "/nonexistent/host.env")
	p := filepath.Join(dir, "inaba.service")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("systemd-analyze", "--user", "verify", p).CombinedOutput(); err != nil {
		t.Errorf("systemd-analyze: %v\n%s", err, out)
	}
}
