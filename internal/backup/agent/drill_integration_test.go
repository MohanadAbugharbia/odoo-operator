package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// drill runs the drill's three steps the way its pod does; odoo stands in
// for the Odoo container and returns its exit code and log ("" skips it, as
// the container does when the load failed).
func (f *fixture) drill(snapshot, suffix string, odoo func(root string) (int, string)) (string, result.Result) {
	f.t.Helper()
	root := f.t.TempDir()
	o := DrillOptions{Suffix: suffix, Snapshot: snapshot, Root: root, ExpectModules: []string{"base", "web", "sale"}, CheckSample: 50}
	if err := DrillLoad(context.Background(), f.env, o); err != nil {
		f.t.Fatalf("drill-load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DrillLoadedFile)); err == nil && odoo != nil {
		code, log := odoo(root)
		hWrite(f.t, filepath.Join(root, DrillOdooLogFile), []byte(log))
		hWrite(f.t, filepath.Join(root, DrillOdooExitFile), []byte(fmt.Sprintf("%d\n", code)))
	}
	return root, DrillFinish(context.Background(), f.env, o)
}

func TestDrill(t *testing.T) {
	f := newFixture(t)
	f.seed(60)
	before := f.state(f.db)
	res := f.mustBackup("first")
	f.mutate()
	live := f.state(f.db)

	// A scratch database an interrupted drill left behind.
	stale := layout.DrillName(f.db, f.nextSuffix())
	f.exec(f.srv.maint, "CREATE DATABASE "+pg.Ident(stale))

	suffix := f.nextSuffix()
	scratch := layout.DrillName(f.db, suffix)
	var seen bool
	_, dr := f.drill(res.SnapshotID, suffix, func(root string) (int, string) {
		// What Odoo is given: the backup as it was, beside the live data.
		seen = true
		if d := diffMaps(before.db, f.fingerprint(scratch)); d != "" {
			t.Fatalf("the scratch database differs from the backup:\n%s", d)
		}
		if d := diffMaps(before.files, files(t, filepath.Join(root, layout.FilestorePath(scratch)))); d != "" {
			t.Fatalf("the scratch filestore differs from the backup:\n%s", d)
		}
		if got := f.databases(); strings.Join(got, ",") != strings.Join(sorted(f.db, scratch), ",") {
			t.Fatalf("databases while Odoo runs: %v (the stale scratch database should be gone)", got)
		}
		return 0, "INFO it_db odoo.modules.loading: Modules loaded.\n"
	})
	if !seen {
		t.Fatal("Odoo did not run after a good load")
	}
	if !dr.OK || dr.OdooVersion != "18.0" || strings.Join(dr.MissingModules, ",") != "sale" {
		t.Fatalf("drill: %+v", dr)
	}
	if got := f.databases(); strings.Join(got, ",") != f.db {
		t.Fatalf("databases after the drill: %v", got)
	}
	f.assertState(f.db, live, "live data after the drill")

	t.Run("Odoo fails", func(t *testing.T) {
		_, dr := f.drill(res.SnapshotID, f.nextSuffix(), func(string) (int, string) {
			return 255, "2026-03-01 10:00:00,000 1 INFO db odoo: start\n" +
				"2026-03-01 10:00:01,000 1 ERROR db odoo.modules.registry: Failed to load registry\n" +
				"Traceback (most recent call last):\n  ...\nKeyError: 'x'\n"
		})
		if dr.OK || dr.Step != result.StepOdoo || !strings.Contains(dr.Error, "exit code 255") ||
			!strings.Contains(dr.Error, "Failed to load registry") {
			t.Fatalf("drill: %+v", dr)
		}
		if got := f.databases(); strings.Join(got, ",") != f.db {
			t.Fatalf("databases after a failed drill: %v", got)
		}
	})

	t.Run("Odoo never reports", func(t *testing.T) {
		_, dr := f.drill(res.SnapshotID, f.nextSuffix(), nil)
		if dr.OK || dr.Step != result.StepOdoo || !strings.Contains(dr.Error, "did not run") {
			t.Fatalf("drill: %+v", dr)
		}
		if got := f.databases(); strings.Join(got, ",") != f.db {
			t.Fatalf("databases: %v", got)
		}
	})

	t.Run("snapshot missing", func(t *testing.T) {
		ran := false
		root, dr := f.drill("deadbeef", f.nextSuffix(), func(string) (int, string) { ran = true; return 0, "" })
		if ran {
			t.Fatal("Odoo ran after a failed load")
		}
		if dr.OK || dr.Step != result.StepFetch {
			t.Fatalf("drill: %+v", dr)
		}
		if _, err := os.Stat(filepath.Join(root, DrillLoadedFile)); !os.IsNotExist(err) {
			t.Fatalf("loaded marker after a failed load: %v", err)
		}
	})
}

func TestDrillFinishWithoutLoad(t *testing.T) {
	root := t.TempDir()
	res := DrillFinish(context.Background(), hEnv(), DrillOptions{Suffix: layout.Suffix(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)), Root: root})
	if res.OK || res.Step != result.StepLoad || !strings.Contains(res.Error, "without reporting") {
		t.Fatalf("finish: %+v", res)
	}
	// The scratch database could not be dropped (no server): said as a warning.
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "could not be dropped") {
		t.Fatalf("warnings: %v", res.Warnings)
	}
}

func TestOdooErrors(t *testing.T) {
	if got := odooErrors("a\nb\nc\nd\ne\nf\ng"); got != "e\nf\ng" {
		t.Fatalf("last lines: %q", got)
	}
	log := "x\n2026 1 ERROR db m: boom\ny\n2026 1 CRITICAL db m: worse\nTraceback (most recent call last):\n  ...\nKeyError: 'z'\n"
	if got := odooErrors(log); got != "2026 1 ERROR db m: boom\n2026 1 CRITICAL db m: worse\nTraceback (most recent call last):\n...\nKeyError: 'z'" {
		t.Fatalf("error lines: %q", got)
	}
}
