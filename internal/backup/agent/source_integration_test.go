/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

// Real-service tests of restores from Odoo's own backup files (testdata/odoo,
// see its README), and of the steps of a restore Job with an Odoo step.

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// odooFixture is what testdata/odoo/<series> holds (see its README).
type odooFixture struct {
	series, sha1, uuid string
}

var odooFixtures = []odooFixture{
	{"17.0", "21c14c1c5f4d89a998aa5b477f781aadfd8856ad", "39f2f2d4-cb43-11f1-9b2c-62b5b900186a"},
	{"18.0", "b1780c43e423d9840c8bbf5f9e8a36580d76b83a", "27eb0a80-cb43-11f1-ad4f-39ab03fe46e7"},
	{"19.0", "96178c33a5fd2422b3d1fe7e0f83f89bb0d44885", "47e442c6-cb43-11f1-94a5-83d59b60ec2e"},
}

// fixturesDir is testdata/odoo, or TEST_ODOO_FIXTURES where the test binary
// runs away from its sources (hack/backup-it.sh).
func fixturesDir() string {
	if d := os.Getenv("TEST_ODOO_FIXTURES"); d != "" {
		return d
	}
	return filepath.Join("testdata", "odoo")
}

// putFile copies a fixture file into the store folder as name.
func putFile(t *testing.T, store, src, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir(), src))
	if err != nil {
		t.Fatal(err)
	}
	hWrite(t, filepath.Join(store, filepath.FromSlash(name)), data)
}

func (f *fixture) restoreFile(store, file, suffix string, opts ...func(*RestoreOptions)) result.Result {
	f.t.Helper()
	o := RestoreOptions{
		Mode: ModeRestore, Suffix: suffix, File: file, StoreTarget: store, DataRoot: f.dataRoot,
		ExpectModules: []string{"base", "web"}, CheckSample: 50,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return Restore(context.Background(), f.env, o)
}

func (f *fixture) queryString(database, sql string) string {
	f.t.Helper()
	var s string
	if err := f.conn(database).QueryRow(context.Background(), sql).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// assertOdooFixture checks the live data is fixture x.
func (f *fixture) assertOdooFixture(x odooFixture, withFiles bool) {
	f.t.Helper()
	if got := f.queryString(f.db, "SELECT latest_version FROM ir_module_module WHERE name = 'base'"); !strings.HasPrefix(got, x.series+".") {
		f.t.Errorf("base is %s, want %s", got, x.series)
	}
	if got := f.queryString(f.db, "SELECT store_fname FROM ir_attachment WHERE id = 14"); got != x.sha1[:2]+"/"+x.sha1 {
		f.t.Errorf("attachment 14 is %s", got)
	}
	if !withFiles {
		return
	}
	data, err := os.ReadFile(filepath.Join(f.filestore(f.db), x.sha1[:2], x.sha1))
	if err != nil || string(data) != "odoo fixture "+x.series+"\n" {
		f.t.Errorf("attachment file = %q, %v", data, err)
	}
}

func TestRestoreOdooZip(t *testing.T) {
	for _, x := range odooFixtures {
		for _, file := range []string{"fixture.zip", "wrapped.zip"} {
			t.Run(x.series+"/"+file, func(t *testing.T) {
				f := newFixture(t)
				store := t.TempDir()
				putFile(t, store, x.series+"/"+file, "exports/"+file)
				rr := f.restoreFile(store, "exports/"+file, f.nextSuffix())
				if !rr.OK || !rr.PreviousKept || rr.File != "exports/"+file || rr.OdooVersion == "" {
					t.Fatalf("restore: %+v", rr)
				}
				f.assertOdooFixture(x, true)
				if got := f.queryString(f.db, "SELECT value FROM ir_config_parameter WHERE key = 'database.uuid'"); got != x.uuid {
					t.Errorf("database.uuid = %s; a restore without neutralize keeps it", got)
				}
				if got := f.databases(); len(got) != 2 {
					t.Errorf("databases: %v (want the restored one and the replaced one)", got)
				}
			})
		}
	}
}

// pgRestoreMajor is the major of the pg_restore the agent reads archives with.
func pgRestoreMajor(t *testing.T, f *fixture) int {
	t.Helper()
	dir, err := f.env.Clients.Newest()
	if err != nil {
		t.Fatal(err)
	}
	bin := "pg_restore"
	if dir != "" {
		bin = filepath.Join(dir, bin)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(\d+)\.`).FindStringSubmatch(string(out))
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestRestoreOdooDump(t *testing.T) {
	for _, x := range odooFixtures {
		t.Run(x.series, func(t *testing.T) {
			f := newFixture(t)
			if pgRestoreMajor(t, f) < 18 {
				t.Skip("the fixtures' .dump files come from pg_dump 18; the newest pg_restore here is older")
			}
			store := t.TempDir()
			putFile(t, store, x.series+"/fixture.dump", "fixture.dump")
			rr := f.restoreFile(store, "fixture.dump", f.nextSuffix())
			if !rr.OK || rr.File != "fixture.dump" {
				t.Fatalf("restore: %+v", rr)
			}
			if !slices.ContainsFunc(rr.Warnings, func(w string) bool { return strings.Contains(w, "holds no filestore") }) {
				t.Errorf("warnings %q do not say the filestore is missing", rr.Warnings)
			}
			f.assertOdooFixture(x, false)
		})
	}
}

// A pattern picks the file whose name sorts last.
func TestRestoreOdooFilePattern(t *testing.T) {
	f := newFixture(t)
	store := t.TempDir()
	putFile(t, store, "18.0/fixture.zip", "exports/shop_2026-01-01.zip")
	putFile(t, store, "19.0/fixture.zip", "exports/shop_2026-02-01.zip")
	putFile(t, store, "17.0/fixture.zip", "exports/other_2026-03-01.zip")
	rr := f.restoreFile(store, "exports/shop_*.zip", f.nextSuffix())
	if !rr.OK || rr.File != "exports/shop_2026-02-01.zip" {
		t.Fatalf("restore: %+v", rr)
	}
	f.assertOdooFixture(odooFixtures[2], true)

	live := f.state(f.db)
	rr = f.restoreFile(store, "exports/none_*.zip", f.nextSuffix())
	if rr.OK || rr.LiveChanged || rr.Step != result.StepFetch || !strings.Contains(rr.Error, "matches") {
		t.Fatalf("restore of a pattern nothing matches: %+v", rr)
	}
	f.assertState(f.db, live, "after the failed restore")
}

// writeZip writes a zip with the given entries.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// What is not a backup of this Odoo is refused before the live data is touched.
func TestRestoreOdooFileRefusals(t *testing.T) {
	const dump = "CREATE TABLE ir_module_module (id serial, name varchar, latest_version varchar, state varchar);\n" +
		"INSERT INTO ir_module_module (name, latest_version, state) VALUES ('base', '18.0.1.3', 'installed');\n"
	cases := map[string]struct {
		file  string
		setup func(t *testing.T, store string)
		step  string
		error string
	}{
		"another series": {
			file:  "old.zip",
			setup: func(t *testing.T, store string) { putFile(t, store, "17.0/fixture.zip", "old.zip") },
			step:  result.StepCheck, error: "17.0",
		},
		"not a zip": {
			file:  "junk.zip",
			setup: func(t *testing.T, store string) { hWrite(t, filepath.Join(store, "junk.zip"), []byte("junk")) },
			step:  result.StepFetch, error: "not a readable zip",
		},
		"no dump.sql": {
			file: "empty.zip",
			setup: func(t *testing.T, store string) {
				writeZip(t, filepath.Join(store, "empty.zip"), map[string]string{"readme.txt": "x"})
			},
			step: result.StepFetch, error: "no dump.sql",
		},
		"two dump.sql": {
			file: "two.zip",
			setup: func(t *testing.T, store string) {
				writeZip(t, filepath.Join(store, "two.zip"), map[string]string{"a/dump.sql": dump, "b/dump.sql": dump})
			},
			step: result.StepFetch, error: "2 dump.sql",
		},
		"a file outside the filestore": {
			file: "slip.zip",
			setup: func(t *testing.T, store string) {
				writeZip(t, filepath.Join(store, "slip.zip"), map[string]string{
					"dump.sql": dump, "filestore/../../../evil": "x",
				})
			},
			step: result.StepLoad, error: "unsafe name",
		},
		"no Odoo database": {
			file: "plain.zip",
			setup: func(t *testing.T, store string) {
				writeZip(t, filepath.Join(store, "plain.zip"), map[string]string{"dump.sql": "CREATE TABLE t (a int);\n"})
			},
			step: result.StepCheck, error: "holds no Odoo database",
		},
	}
	for name, cs := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(2) // an 18.0 database
			live := f.state(f.db)
			store := t.TempDir()
			cs.setup(t, store)
			rr := f.restoreFile(store, cs.file, f.nextSuffix())
			if rr.OK || rr.LiveChanged || rr.Step != cs.step || !strings.Contains(rr.Error, cs.error) {
				t.Fatalf("restore: %+v; want step %s and an error with %q", rr, cs.step, cs.error)
			}
			f.assertState(f.db, live, "after the refused restore")
			if got := f.databases(); strings.Join(got, ",") != f.db {
				t.Errorf("databases: %v", got)
			}
			if _, err := os.Stat(filepath.Join(f.dataRoot, "..", "evil")); err == nil {
				t.Error("the zip wrote outside the data folder")
			}
		})
	}
}

// The three steps of a restore Job with an Odoo step: the load step stops
// before the swap, the Odoo step (simulated) reports its version and
// neutralizes, the last step checks both and swaps.
func TestRestoreWithOdooStep(t *testing.T) {
	x := odooFixtures[1]
	setup := func(t *testing.T) (*fixture, string, string, string) {
		f := newFixture(t)
		store := t.TempDir()
		putFile(t, store, x.series+"/fixture.zip", "fixture.zip")
		work := t.TempDir()
		suffix := f.nextSuffix()
		rr := f.restoreFile(store, "fixture.zip", suffix, func(o *RestoreOptions) { o.WorkDir, o.LoadOnly = work, true })
		if !rr.OK || rr.LiveChanged {
			t.Fatalf("load step: %+v", rr)
		}
		source, err := os.ReadFile(filepath.Join(work, WorkSource))
		if err != nil || string(source) != layout.RestoreName(f.db, suffix) {
			t.Fatalf("the load step named %q (%v) for the Odoo step", source, err)
		}
		return f, store, work, suffix
	}
	last := func(f *fixture, store, work, suffix string, neutralize bool) result.Result {
		return f.restoreFile(store, "fixture.zip", suffix, func(o *RestoreOptions) { o.WorkDir, o.Neutralize = work, neutralize })
	}

	t.Run("neutralized", func(t *testing.T) {
		f, store, work, suffix := setup(t)
		hWrite(t, filepath.Join(work, WorkOdooVersion), []byte("Odoo Server 18.0-20261015\n"))
		hWrite(t, filepath.Join(work, WorkNeutralizeExit), []byte("0\n"))
		rr := last(f, store, work, suffix, true)
		if !rr.OK || !rr.LiveChanged {
			t.Fatalf("last step: %+v", rr)
		}
		f.assertOdooFixture(x, true)
		uuid := f.queryString(f.db, "SELECT value FROM ir_config_parameter WHERE key = 'database.uuid'")
		if uuid == x.uuid || len(uuid) != 36 {
			t.Errorf("database.uuid = %s; neutralize gives a new one", uuid)
		}
	})
	t.Run("Odoo of another series", func(t *testing.T) {
		f, store, work, suffix := setup(t)
		live := f.state(f.db)
		hWrite(t, filepath.Join(work, WorkOdooVersion), []byte("Odoo Server 19.0-20261015\n"))
		rr := last(f, store, work, suffix, false)
		if rr.OK || rr.LiveChanged || rr.Step != result.StepCheck || !strings.Contains(rr.Error, "19.0") {
			t.Fatalf("last step: %+v", rr)
		}
		f.assertState(f.db, live, "after the refused restore")
		if got := f.databases(); strings.Join(got, ",") != f.db {
			t.Errorf("the loaded database was not dropped: %v", got)
		}
	})
	t.Run("neutralize failed", func(t *testing.T) {
		f, store, work, suffix := setup(t)
		live := f.state(f.db)
		hWrite(t, filepath.Join(work, WorkOdooVersion), []byte("Odoo Server 18.0-20261015\n"))
		hWrite(t, filepath.Join(work, WorkNeutralizeExit), []byte("1\n"))
		rr := last(f, store, work, suffix, true)
		if rr.OK || rr.LiveChanged || !strings.Contains(rr.Error, "neutralize") {
			t.Fatalf("last step: %+v", rr)
		}
		f.assertState(f.db, live, "after the failed neutralize")
	})
	t.Run("load step failed", func(t *testing.T) {
		f := newFixture(t)
		work := t.TempDir()
		failed := result.Failed(result.StepFetch, os.ErrNotExist)
		hWrite(t, filepath.Join(work, WorkLoadResult), result.Encode(failed))
		rr := f.restoreFile(t.TempDir(), "fixture.zip", f.nextSuffix(), func(o *RestoreOptions) { o.WorkDir = work })
		if rr.OK || rr.LiveChanged || rr.Step != result.StepFetch {
			t.Fatalf("last step after a failed load: %+v", rr)
		}
	})
}

// Another instance's repository, read without locks (a readOnly store), with
// "latest" narrowed by host and tags.
func TestRestoreLatestFromAnotherRepository(t *testing.T) {
	src := newFixture(t)
	src.seed(3)
	src.mustBackup("first")
	src.mutate()
	want := src.state(src.db)
	src.mustBackup("second")
	src.mutate()
	src.mustBackup("other", func(o *BackupOptions) { o.Kind = "scheduled" })

	f := newFixture(t)
	f.seed(1)
	reader := *src.env.Restic
	reader.NoLock, reader.RetryLock = true, 0
	f.env.Restic = &reader
	locks := filepath.Join(src.repo, "locks")
	before, _ := os.ReadDir(locks)
	rr := Restore(context.Background(), f.env, RestoreOptions{
		Mode: ModeRestore, Suffix: f.nextSuffix(), Snapshot: "latest",
		Host: layout.Host(itNamespace, itDeployment), Tags: []string{layout.TagKind + "manual"},
		DataRoot: f.dataRoot, ExpectModules: []string{"base"}, CheckSample: 50,
	})
	if !rr.OK || len(rr.SnapshotID) != 64 {
		t.Fatalf("restore: %+v", rr)
	}
	f.assertState(f.db, want, "restored from the latest manual backup")
	if after, _ := os.ReadDir(locks); len(after) != len(before) {
		t.Errorf("the restore left %d lock files in the repository (before: %d)", len(after), len(before))
	}

	rr = Restore(context.Background(), f.env, RestoreOptions{
		Mode: ModeRestore, Suffix: f.nextSuffix(), Snapshot: "latest", Host: "elsewhere/odoo",
		DataRoot: f.dataRoot, ExpectModules: []string{"base"},
	})
	if rr.OK || rr.LiveChanged || rr.Step != result.StepFetch || !strings.Contains(rr.Error, "no snapshots") {
		t.Fatalf("restore of a host with no snapshots: %+v", rr)
	}
}
