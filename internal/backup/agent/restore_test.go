package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// Finishing a swap needs only the journal; when the journal shows the swap
// has not started, it fails before touching the database and reports the
// live data unchanged, whatever source options it was given.
func TestFinishSwapRefusesWhenTheSwapHasNotStarted(t *testing.T) {
	suffix := layout.Suffix(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name    string
		mode    string
		journal string
		stage   string
	}{
		{"no journal", ModeRestore, "", "none"},
		{"loaded, not swapped", ModeRestore, `{"version":1,"mode":"restore","suffix":"` + suffix + `","stage":"restored"}`, "restored"},
		{"undo without the undone restore's suffix", ModeUndo, `{"version":1,"mode":"undo","suffix":"` + suffix + `","stage":"restored"}`, "restored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.journal != "" {
				path := filepath.Join(root, layout.JournalFile(suffix))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.journal), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// No database is reachable: a run that tried one would fail at
			// step connect instead.
			env := &Env{Config: odooconf.Config{Name: "shop", Host: "127.0.0.1", Port: 1}}
			res := Restore(context.Background(), env, RestoreOptions{
				Mode: tc.mode, Suffix: suffix, DataRoot: root, FinishSwap: true, Snapshot: "latest",
			})
			if res.OK || res.Step != result.StepSwap || res.LiveChanged {
				t.Fatalf("got %+v, want a failure at step swap with the live data unchanged", res)
			}
			if want := "swap has not started (stage " + tc.stage + ")"; !strings.Contains(res.Error, want) {
				t.Fatalf("error %q does not say %q", res.Error, want)
			}
			if _, err := os.Stat(filepath.Join(root, layout.JournalFile(suffix))); tc.journal == "" && !os.IsNotExist(err) {
				t.Fatalf("a journal was written: %v", err)
			}
		})
	}
}
