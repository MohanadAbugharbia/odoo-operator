package agent

// Real-service tests of zip copies and of exports: a backup's Odoo zip in
// every kind of store, restored by the operator, and deleted when retention
// forgets its backup.

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/retention"
)

// zipNames lists the zips (and anything else) in an instance's folder.
func zipNames(t *testing.T, target, folder string) []string {
	t.Helper()
	out, err := rcloneRun(context.Background(), "", "lsf", "--files-only", joinTarget(target, folder))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

// checkZipLayout fetches a zip and checks it holds dump.sql, manifest.json
// and the backup's filestore files.
func checkZipLayout(t *testing.T, remote string, wantFiles int64) {
	t.Helper()
	local := filepath.Join(t.TempDir(), "copy.zip")
	if _, err := rcloneRun(context.Background(), "", "copyto", remote, local); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(local)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	names := map[string]bool{}
	var filestoreFiles int64
	for _, zf := range zr.File {
		names[zf.Name] = true
		if strings.HasPrefix(zf.Name, "filestore/") {
			filestoreFiles++
		}
	}
	if !names["dump.sql"] || !names["manifest.json"] || filestoreFiles != wantFiles {
		t.Fatalf("zip holds %v (want dump.sql, manifest.json and %d filestore files)", names, wantFiles)
	}
}

// zipStoreJob is the zip store of a store case and its emulated copy target.
func zipStoreJob(t *testing.T, tc storeCase) jobEnvironment {
	t.Helper()
	store := tc.store.DeepCopy()
	store.Name = "zips"
	store.Spec.Format = odoov1.BackupStoreFormatZip
	store.Spec.EncryptionKeySecret = nil
	switch sp := &store.Spec; {
	case sp.S3 != nil:
		sp.S3.Path += "-zips"
	case sp.SFTP != nil:
		sp.SFTP.Path += "-zips"
	}
	loc, err := jobspec.LocateCopy(store, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range loc.Env {
		if strings.Contains(e.Name, "RESTIC") {
			t.Fatalf("a zip store's Job gets %s", e.Name)
		}
	}
	je := emulateLocation(t, loc, tc.secrets, filepath.Join(t.TempDir(), "zips"))
	if tc.name == "pvc" || tc.name == "nfs" {
		if err := os.MkdirAll(je.probeTarget, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return je
}

func TestZipCopies(t *testing.T) {
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(12)
			je := zipStoreJob(t, tc)
			setEnv(t, append(je.env, "COPY1_ZIP_TARGET="+je.probeTarget))
			targets, err := CopyTargetsFromEnv(f.env.Restic, []string{"zips"})
			if err != nil {
				t.Fatal(err)
			}
			ret := &Retention{Policy: retention.Policy{Last: 1}}
			withCopies := func(o *BackupOptions) { o.Kind = retention.KindScheduled; o.Copies = targets; o.Retention = ret }
			folder := layout.Host(itNamespace, itDeployment)

			res1 := f.mustBackup("one", withCopies)
			c1 := res1.Copies[0]
			if c1.Error != "" || c1.SnapshotID != "" || path.Dir(c1.File) != folder ||
				!strings.HasPrefix(path.Base(c1.File), f.db+"_") || !strings.HasSuffix(c1.File, ".zip") {
				t.Fatalf("zip copy: %+v\n%s", res1.Copies, f.logs.String())
			}
			if got := zipNames(t, je.probeTarget, folder); len(got) != 1 || got[0] != path.Base(c1.File) {
				t.Fatalf("store holds %v", got)
			}

			// The zip is Odoo's own layout and the backup's contents.
			checkZipLayout(t, joinTarget(je.probeTarget, c1.File), res1.FilestoreFiles)

			// An upload killed mid-way left its temporary file, which no
			// retry overwrites (a retry's zip has a new name).
			partial := path.Join(folder, "."+ZipName(f.db, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))+".partial")
			if _, err := rcloneRun(context.Background(), "", "touch", joinTarget(je.probeTarget, partial)); err != nil {
				t.Fatal(err)
			}
			if got := zipNames(t, je.probeTarget, folder); len(got) != 2 {
				t.Fatalf("store holds %v, want the zip and the partial upload", got)
			}

			// A second backup in another second: retention forgets the first
			// and its zip goes, and so does the partial upload.
			time.Sleep(1100 * time.Millisecond)
			before := f.state(f.db)
			res2 := f.mustBackup("two", withCopies)
			c2 := res2.Copies[0]
			if c2.Error != "" || c2.File == c1.File || len(res2.Warnings) != 0 {
				t.Fatalf("second zip copy: %+v %v", res2.Copies, res2.Warnings)
			}
			if got := zipNames(t, je.probeTarget, folder); len(got) != 1 || got[0] != path.Base(c2.File) {
				t.Fatalf("after retention the store holds %v, want only %s", got, path.Base(c2.File))
			}

			// The operator restores the zip as any Odoo backup file.
			f.mutate()
			rr := f.restoreFile(je.probeTarget, c2.File, f.nextSuffix(), func(o *RestoreOptions) {
				o.ExpectModules = []string{"base", "web", "sale"}
			})
			if !rr.OK || rr.File != c2.File {
				t.Fatalf("restore of the zip: %+v\n%s", rr, f.logs.String())
			}
			f.assertState(f.db, before, "restored from the zip copy in "+tc.name)
		})
	}
}

func TestExport(t *testing.T) {
	f := newFixture(t)
	f.seed(6)
	res := f.mustBackup("one")
	host := layout.Host(itNamespace, itDeployment)

	local := filepath.Join(t.TempDir(), "out.zip")
	er := Export(context.Background(), f.env, ExportOptions{Snapshot: "latest", Host: host, Output: local})
	if !er.OK || er.SnapshotID != res.SnapshotID || er.File != local {
		t.Fatalf("export to a file: %+v", er)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	got := hUnzip(t, data)
	if !strings.Contains(got["dump.sql"], "CREATE TABLE") || !strings.Contains(got["manifest.json"], `"db_name"`) {
		t.Fatalf("exported zip holds %d entries without a usable dump or manifest", len(got))
	}
	if entries, _ := os.ReadDir(filepath.Dir(local)); len(entries) != 1 {
		t.Fatalf("export left %v", entries)
	}

	// To stdout, and to a folder target, where it is named as zip copies are.
	var buf bytes.Buffer
	if er := Export(context.Background(), f.env, ExportOptions{Snapshot: res.SnapshotID, Output: "-", Stdout: &buf}); !er.OK {
		t.Fatalf("export to stdout: %+v", er)
	}
	if d := diffMaps(got, hUnzip(t, buf.Bytes())); d != "" {
		t.Fatalf("stdout export differs: %s", d)
	}
	dir := t.TempDir()
	er = Export(context.Background(), f.env, ExportOptions{Snapshot: res.SnapshotID, Target: dir + "/"})
	if !er.OK || path.Dir(er.File) != dir || !strings.HasPrefix(path.Base(er.File), f.db+"_") {
		t.Fatalf("export to a folder: %+v", er)
	}
	if _, err := os.Stat(er.File); err != nil {
		t.Fatal(err)
	}

	// An unknown snapshot fails at the fetch step.
	if er := Export(context.Background(), f.env, ExportOptions{Snapshot: "deadbeef", Output: local}); er.OK || er.Step != result.StepFetch {
		t.Fatalf("export of an unknown snapshot: %+v", er)
	}
}
