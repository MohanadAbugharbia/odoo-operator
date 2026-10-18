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

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/manifest"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// BackupOptions describe one backup.
type BackupOptions struct {
	// Root is the folder backed up: it holds the filestore (a read-only
	// mount in the Job) and receives dump.sql and manifest.json. Its path is
	// the path inside the snapshot (layout.BackupRoot in the Job).
	Root string

	Namespace  string
	Deployment string
	Backup     string
	Kind       string
	// Image and UpgradeToken go into the manifest; a restore starts Odoo on them.
	Image        string
	UpgradeToken string
	Consistency  string
	// Version is the backup tool's version.
	Version string

	// WaitForCleanup defers the backup (Result.Deferred) while Odoo's
	// filestore cleanup runs or is due within CleanupWindow.
	WaitForCleanup bool
	CleanupWindow  time.Duration

	// Retention, when set, thins the repository after the backup.
	Retention *Retention
	// Copies are the other stores the new snapshot is copied to.
	Copies []CopyTarget
}

// maxListed is how many file names a Result lists.
const maxListed = 20

// Backup takes one backup: dump and file list from one database snapshot,
// the filestore, a check that every attachment is in the backup, and one
// restic snapshot holding all of it.
//
//nolint:gocyclo // one linear sequence of steps
func Backup(ctx context.Context, e *Env, o BackupOptions) result.Result {
	conn, err := pg.Connect(ctx, e.Config, e.Config.Name)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	serverVersion, err := pg.ServerVersionNum(ctx, conn)
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}
	binDir, err := e.Clients.Pick(pg.Major(serverVersion))
	if err != nil {
		return result.Failed(result.StepConnect, err)
	}

	if o.WaitForCleanup {
		av, err := pg.CheckAutovacuum(ctx, conn)
		if err != nil {
			return result.Failed(result.StepWait, err)
		}
		if wait, why := av.ShouldWait(o.CleanupWindow); wait {
			e.logf("not starting yet: %s", why)
			return result.Result{OK: true, Deferred: true, Warnings: []string{why}}
		}
	}

	if created, err := e.Restic.EnsureRepository(ctx); err != nil {
		return result.Failed(result.StepRepo, err)
	} else if created {
		e.logf("created the repository")
	}
	if err := e.Restic.Unlock(ctx); err != nil {
		return result.Failed(result.StepRepo, err)
	}
	host := layout.Host(o.Namespace, o.Deployment)
	previous, err := e.Restic.Snapshots(ctx, host, layout.TagDeployment+host)
	if err != nil {
		return result.Failed(result.StepRepo, err)
	}

	// Everything that describes the database comes from one snapshot.
	snap, err := pg.ExportSnapshot(ctx, conn)
	if err != nil {
		return result.Failed(result.StepDump, err)
	}
	defer snap.Close(context.Background())
	e.logf("database snapshot %s taken at %s", snap.ID, snap.Time.UTC().Format(time.RFC3339))
	names, err := pg.StoreNames(ctx, snap.Tx)
	if err != nil {
		return result.Failed(result.StepDump, err)
	}
	counts, err := pg.RowCounts(ctx, snap.Tx)
	if err != nil {
		return result.Failed(result.StepDump, err)
	}
	modules, err := pg.InstalledModules(ctx, snap.Tx)
	if err != nil {
		return result.Failed(result.StepDump, err)
	}
	dumpPath := filepath.Join(o.Root, layout.DumpFile)
	dumpSum, dumpBytes, pgDumpVersion, err := dump(ctx, e, binDir, snap.ID, dumpPath)
	if err != nil {
		return result.Failed(result.StepDump, err)
	}
	snap.Close(ctx)
	e.logf("dumped %d bytes with %s", dumpBytes, pgDumpVersion)
	if err := e.fault(FaultBackupDumped); err != nil {
		return result.Failed(result.StepDump, err)
	}

	fsDir := filepath.Join(o.Root, layout.FilestoreDir)
	files, fsBytes, err := walkFilestore(fsDir)
	if err != nil {
		return result.Failed(result.StepFilestore, err)
	}
	var missing, unsafe []string
	for _, n := range names {
		switch {
		case !safeStoreName(n):
			unsafe = append(unsafe, n)
		case !isFile(filepath.Join(fsDir, n)):
			missing = append(missing, n)
		}
	}
	repairedDir := filepath.Join(o.Root, layout.RepairedDir)
	if err := os.RemoveAll(repairedDir); err != nil {
		return result.Failed(result.StepFilestore, err)
	}
	repaired, stillMissing, err := repair(ctx, e, o.Root, previous, missing)
	if err != nil {
		return result.Failed(result.StepFilestore, err)
	}
	e.logf("filestore: %d files, %d bytes; %d attachments, %d taken from the previous backup, %d missing",
		files, fsBytes, len(names), len(repaired), len(stillMissing))

	m := manifest.New(e.Config.Name, modules, serverVersion)
	m.Operator = &manifest.Operator{
		Version:            o.Version,
		Namespace:          o.Namespace,
		Deployment:         o.Deployment,
		Backup:             o.Backup,
		Kind:               o.Kind,
		Image:              o.Image,
		UpgradeToken:       o.UpgradeToken,
		Consistency:        o.Consistency,
		SnapshotTime:       snap.Time.UTC(),
		PgServerVersionNum: serverVersion,
		PgDump:             pgDumpVersion,
		DumpSHA256:         dumpSum,
		DumpBytes:          dumpBytes,
		RowCounts:          counts,
		Files: manifest.Files{
			Referenced: len(names),
			Repaired:   repaired,
			Missing:    append(append([]string(nil), stillMissing...), unsafe...),
		},
	}
	data, err := m.Encode()
	if err != nil {
		return result.Failed(result.StepUpload, err)
	}
	if err := os.WriteFile(filepath.Join(o.Root, layout.ManifestFile), data, 0o640); err != nil {
		return result.Failed(result.StepUpload, err)
	}

	up, err := e.Restic.Backup(ctx, restic.BackupOptions{
		Host: host,
		Tags: layout.Tags(o.Namespace, o.Deployment, o.Backup, o.Kind),
		Time: snap.Time,
	}, o.Root)
	if err != nil {
		return result.Failed(result.StepUpload, err)
	}
	e.logf("snapshot %s: %d bytes added", up.Summary.SnapshotID, up.Summary.DataAdded)
	if err := e.fault(FaultBackupUploaded); err != nil {
		return result.Failed(result.StepUpload, err)
	}

	// The snapshot must hold dump.sql, manifest.json and every attachment
	// that was in the filestore when it was checked: a file deleted after
	// the check (Odoo's cleanup despite the wait) shows up here.
	lost, err := verifySnapshot(ctx, e, up.Summary.SnapshotID, o.Root, names, stillMissing, unsafe, repaired)
	if err != nil {
		return result.Failed(result.StepVerify, err)
	}
	allMissing := append(append(append([]string(nil), stillMissing...), unsafe...), lost...)
	sort.Strings(allMissing)

	res := result.Result{
		OK:              true,
		SnapshotID:      up.Summary.SnapshotID,
		OdooVersion:     m.Series(),
		PgServerVersion: manifest.ServerVersion(serverVersion),
		DatabaseBytes:   dumpBytes,
		FilestoreBytes:  fsBytes,
		FilestoreFiles:  files,
		AddedBytes:      up.Summary.DataAdded,
		RepairedFiles:   len(repaired),
		MissingFiles:    len(allMissing),
		MissingNames:    limit(allMissing, maxListed),
	}
	if len(allMissing) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d attachment(s) the database refers to are in neither the filestore nor the previous backup", len(allMissing)))
	}
	if len(unsafe) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d attachment(s) have a file name that is not a plain path and were skipped", len(unsafe)))
	}
	if len(up.Unreadable) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("restic could not read %d file(s), e.g. %s", len(up.Unreadable), up.Unreadable[0]))
	}

	// Retention runs only once the new snapshot is known to be good, and a
	// failure leaves the backup taken.
	var forgotten []restic.Snapshot
	if o.Retention != nil {
		forgotten, err = applyRetention(ctx, e, e.Restic, host, up.Summary.SnapshotID, o.Retention)
		if err != nil {
			e.logf("retention failed: %v", err)
			res.RetentionError = tail(err.Error())
		}
		res.Forgotten = snapshotIDs(forgotten)
	}
	if len(o.Copies) > 0 {
		var warnings []string
		res.Copies, warnings = copyAll(ctx, e, o.Copies, host, o.Backup, up.Summary.SnapshotID, o.Retention, forgotten)
		res.Warnings = append(res.Warnings, warnings...)
	}
	return res
}

// dump runs pg_dump against the exported snapshot into path and returns the
// dump's sha256, size and the pg_dump version.
func dump(ctx context.Context, e *Env, binDir, snapshotID, path string) (string, int64, string, error) {
	pgDump := pg.Tool(binDir, "pg_dump")
	versionOut, err := exec.CommandContext(ctx, pgDump, "--version").Output()
	if err != nil {
		return "", 0, "", fmt.Errorf("%s --version: %w", pgDump, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	counter := &countingWriter{}
	cmd := exec.CommandContext(ctx, pgDump,
		"--format=plain", "--no-owner", "--snapshot="+snapshotID, "--lock-wait-timeout=600000")
	cmd.Env = append(os.Environ(), e.Config.Env(e.Config.Name)...)
	cmd.Stdout = io.MultiWriter(f, h, counter)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 30 * time.Second
	if err := cmd.Run(); err != nil {
		return "", 0, "", fmt.Errorf("pg_dump: %w: %s", err, tail(stderr.String()))
	}
	if err := f.Sync(); err != nil {
		return "", 0, "", err
	}
	return hex.EncodeToString(h.Sum(nil)), counter.n, string(bytes.TrimSpace(versionOut)), nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// tail returns the end of a command's stderr for an error message.
func tail(s string) string {
	s = string(bytes.TrimSpace([]byte(s)))
	if len(s) > 1500 {
		return "…" + s[len(s)-1500:]
	}
	return s
}

// walkFilestore counts the files and bytes under dir. A missing dir is an
// empty filestore.
func walkFilestore(dir string) (int64, int64, error) {
	var files, size int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == dir {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // deleted meanwhile
				}
				return err
			}
			files++
			size += info.Size()
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	return files, size, err
}

// repair takes attachments missing from the filestore from the previous
// snapshot (a filestore name is the hash of its content, so the same name is
// the same file) into <root>/filestore-repaired/. It returns the names it
// repaired and the ones it could not.
func repair(ctx context.Context, e *Env, root string, previous []restic.Snapshot, missing []string) ([]string, []string, error) {
	if len(missing) == 0 {
		return nil, nil, nil
	}
	if len(previous) == 0 {
		return nil, missing, nil
	}
	prev := previous[len(previous)-1]
	if len(prev.Paths) != 1 {
		return nil, missing, nil
	}
	prevRoot := prev.Paths[0]
	includes := make([]string, 0, 2*len(missing))
	for _, n := range missing {
		includes = append(includes,
			filepath.Join(prevRoot, layout.FilestoreDir, n),
			filepath.Join(prevRoot, layout.RepairedDir, n))
	}
	tmp, err := os.MkdirTemp("", "repair-*")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	e.logf("taking %d missing attachment(s) from snapshot %s", len(missing), prev.ShortID)
	if err := e.Restic.Restore(ctx, prev.ID, tmp, includes...); err != nil {
		return nil, nil, fmt.Errorf("restore missing attachments from snapshot %s: %w", prev.ShortID, err)
	}
	var repaired, still []string
	for _, n := range missing {
		var found string
		for _, sub := range []string{layout.FilestoreDir, layout.RepairedDir} {
			candidate := filepath.Join(tmp, prevRoot, sub, n)
			if !isFile(candidate) {
				continue
			}
			if ok, err := contentMatchesName(candidate); err != nil || !ok {
				continue
			}
			found = candidate
			break
		}
		if found == "" {
			still = append(still, n)
			continue
		}
		dst := filepath.Join(root, layout.RepairedDir, n)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return nil, nil, err
		}
		if err := moveFile(found, dst); err != nil {
			return nil, nil, err
		}
		repaired = append(repaired, n)
	}
	return repaired, still, nil
}

// moveFile renames src to dst, copying when they are on different filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// verifySnapshot lists the new snapshot and returns the attachments that
// were present when checked but did not make it into the snapshot.
func verifySnapshot(ctx context.Context, e *Env, snapshotID, root string,
	names, known, unsafe, repaired []string) ([]string, error) {
	inSnapshot := map[string]bool{}
	topLevel := map[string]bool{}
	err := e.Restic.Ls(ctx, snapshotID, root, func(n restic.Node) {
		rel, err := filepath.Rel(root, n.Path)
		if err != nil || n.Type != "file" {
			return
		}
		topLevel[rel] = true
		inSnapshot[rel] = true
	})
	if err != nil {
		return nil, err
	}
	for _, f := range []string{layout.DumpFile, layout.ManifestFile} {
		if !topLevel[f] {
			return nil, fmt.Errorf("snapshot %s has no %s", snapshotID, f)
		}
	}
	skip := map[string]bool{}
	for _, n := range append(append(append([]string(nil), known...), unsafe...), repaired...) {
		skip[n] = true
	}
	var lost []string
	for _, n := range names {
		if skip[n] {
			if contains(repaired, n) && !inSnapshot[filepath.Join(layout.RepairedDir, n)] {
				lost = append(lost, n)
			}
			continue
		}
		if !inSnapshot[filepath.Join(layout.FilestoreDir, n)] {
			lost = append(lost, n)
		}
	}
	return lost, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
