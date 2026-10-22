package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// A restore that cannot tell from its journal how far the swap got must
// not report the live data as untouched: the controller ends a restore as
// failed, with nothing to undo, on that report.
func TestRestoreJournalItCannotUseKeepsLiveChanged(t *testing.T) {
	suffix := layout.Suffix(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name    string
		journal string
		want    bool
	}{
		{"unreadable", "{not json", true},
		{"another mode, swap started", `{"version":1,"mode":"undo","suffix":"` + suffix + `","stage":"swapping"}`, true},
		{"another mode, swap not started", `{"version":1,"mode":"undo","suffix":"` + suffix + `","stage":"restored"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, layout.JournalFile(suffix))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.journal), 0o600); err != nil {
				t.Fatal(err)
			}
			env := &Env{Config: odooconf.Config{Name: "shop"}}
			res := Restore(context.Background(), env, RestoreOptions{Mode: ModeRestore, Suffix: suffix, DataRoot: root})
			if res.OK || res.Step != result.StepConnect {
				t.Fatalf("got %+v, want a failure at step connect", res)
			}
			if res.LiveChanged != tc.want {
				t.Fatalf("LiveChanged = %v, want %v (%s)", res.LiveChanged, tc.want, res.Error)
			}
		})
	}
}
