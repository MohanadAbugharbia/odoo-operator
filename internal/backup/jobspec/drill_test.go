package jobspec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/agent"
)

// runDrillScript runs the Odoo container's script with a command in place
// of Odoo, as the container would.
func runDrillScript(t *testing.T, root string, command ...string) {
	t.Helper()
	args := append([]string{"-c", drillScript, "sh"}, command...)
	cmd := exec.Command("sh", args...)
	cmd.Env = append(os.Environ(), "DRILL_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script must exit 0: %v\n%s", err, out)
	}
}

func TestDrillScript(t *testing.T) {
	read := func(root, name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return strings.TrimSpace(string(data))
	}

	// The load failed: Odoo does not run.
	root := t.TempDir()
	runDrillScript(t, root, "sh", "-c", "touch \"$DRILL_ROOT/ran\"")
	if _, err := os.Stat(filepath.Join(root, "ran")); !os.IsNotExist(err) {
		t.Fatal("Odoo ran without a loaded database")
	}
	if _, err := os.Stat(filepath.Join(root, agent.DrillOdooExitFile)); !os.IsNotExist(err) {
		t.Fatal("an exit code was written for an Odoo that did not run")
	}

	// The load passed: Odoo runs with its arguments, its exit code and log
	// are kept, and the script still exits 0.
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, agent.DrillLoadedFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runDrillScript(t, root, "sh", "-c", "echo \"loading $1\"; echo ERROR boom >&2; exit 3", "odoo", "-d x_drill_1")
	if got := read(root, agent.DrillOdooExitFile); got != "3" {
		t.Fatalf("exit code %q", got)
	}
	if got := read(root, agent.DrillOdooLogFile); got != "loading -d x_drill_1\nERROR boom" {
		t.Fatalf("log %q", got)
	}

	runDrillScript(t, root, "true")
	if got := read(root, agent.DrillOdooExitFile); got != "0" {
		t.Fatalf("exit code %q", got)
	}
}
