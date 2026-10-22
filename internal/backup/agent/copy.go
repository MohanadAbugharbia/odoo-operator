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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// CopyTarget is another store a backup is copied to.
type CopyTarget struct {
	// Store is the OdooBackupStore's name.
	Store string
	// Restic runs restic against the instance's repository in that store.
	Restic *restic.Restic
	// ZipTarget, for a store of format zip, is the store's root as rclone
	// names it: the backup is written there as an Odoo zip instead.
	ZipTarget string
	// Rclone is the rclone executable; "rclone" when empty.
	Rclone string
}

// CopyTargetsFromEnv builds the targets of a backup Job's --copy stores:
// the n-th store's repository and password file are in COPY<n>_RESTIC_REPOSITORY
// and COPY<n>_RESTIC_PASSWORD_FILE (jobspec.LocateCopy), and restic is
// otherwise run as for the instance's own store. A zip store has
// COPY<n>_ZIP_TARGET instead.
func CopyTargetsFromEnv(base *restic.Restic, stores []string) ([]CopyTarget, error) {
	out := make([]CopyTarget, 0, len(stores))
	for i, store := range stores {
		prefix := fmt.Sprintf("COPY%d_", i+1)
		if target := os.Getenv(prefix + "ZIP_TARGET"); target != "" {
			out = append(out, CopyTarget{Store: store, ZipTarget: target})
			continue
		}
		repo, pw := os.Getenv(prefix+"RESTIC_REPOSITORY"), os.Getenv(prefix+"RESTIC_PASSWORD_FILE")
		if repo == "" || pw == "" {
			return nil, fmt.Errorf("copy to %s needs %sRESTIC_REPOSITORY and %sRESTIC_PASSWORD_FILE", store, prefix, prefix)
		}
		r := *base
		r.Env = append(append([]string(nil), base.Env...), "RESTIC_REPOSITORY="+repo, "RESTIC_PASSWORD_FILE="+pw, "RESTIC_PASSWORD=")
		out = append(out, CopyTarget{Store: store, Restic: &r})
	}
	return out, nil
}

// copySnapshot copies snapshot id from the instance's repository into the
// target's (creating it with the same chunker parameters, so the copy stays
// deduplicated), applies retention there, and returns the copy's ID. A
// snapshot copied before is found, not copied again.
func copySnapshot(ctx context.Context, e *Env, t CopyTarget, host, backup, id string, ret *Retention) (string, error) {
	if created, err := t.Restic.EnsureCopyOf(ctx, e.Restic); err != nil {
		return "", fmt.Errorf("open the repository: %w", err)
	} else if created {
		e.logf("copy to %s: created the repository", t.Store)
	}
	if err := t.Restic.Unlock(ctx); err != nil {
		return "", err
	}
	if err := t.Restic.CopyFrom(ctx, e.Restic, id); err != nil {
		return "", err
	}
	snaps, err := t.Restic.Snapshots(ctx, host, layout.TagDeployment+host, layout.TagBackup+backup)
	if err != nil {
		return "", err
	}
	copyID := ""
	for _, s := range snaps {
		if s.Original == id || s.ID == id {
			copyID = s.ID
		}
	}
	if copyID == "" {
		return "", fmt.Errorf("restic copy left no copy of snapshot %s", id[:min(8, len(id))])
	}
	e.logf("copy to %s: snapshot %s", t.Store, copyID[:min(8, len(copyID))])
	if ret != nil {
		if _, err := applyRetention(ctx, e, t.Restic, host, copyID, ret); err != nil {
			// The copy is there; retention will run again after the next backup.
			e.logf("copy to %s: retention failed: %v", t.Store, err)
		}
	}
	return copyID, nil
}

// copyAll copies the new snapshot to every target, one after the other. A
// zip store gets the snapshot as an Odoo zip, and loses the zips of the
// snapshots retention forgot; failing to delete one is a warning.
func copyAll(ctx context.Context, e *Env, targets []CopyTarget, host, backup, id string, ret *Retention,
	forgotten []restic.Snapshot) ([]result.Copy, []string) {
	out := make([]result.Copy, 0, len(targets))
	var warnings []string
	for _, t := range targets {
		c := result.Copy{Store: t.Store}
		var err error
		if t.ZipTarget != "" {
			c.File, err = copyZip(ctx, e, t, host, id)
			if err == nil {
				if werr := deleteZips(ctx, e, t, host, forgotten, path.Base(c.File)); werr != nil {
					e.logf("copy to %s: %v", t.Store, werr)
					warnings = append(warnings, fmt.Sprintf("store %s: %s", t.Store, tail(werr.Error())))
				}
			}
		} else {
			c.SnapshotID, err = copySnapshot(ctx, e, t, host, backup, id, ret)
		}
		if err != nil {
			e.logf("copy to %s failed: %v", t.Store, err)
			c.Error = tail(err.Error())
		}
		out = append(out, c)
	}
	return out, warnings
}

// copyZip writes snapshot id as an Odoo zip into the instance's folder
// (folder, <namespace>/<name>) of a zip store and returns its path there.
func copyZip(ctx context.Context, e *Env, t CopyTarget, folder, id string) (string, error) {
	snap, err := e.Restic.Snapshot(ctx, id)
	if err != nil {
		return "", err
	}
	file := path.Join(folder, ZipName(e.Config.Name, snap.Time))
	files, err := uploadZip(ctx, t.Rclone, joinTarget(t.ZipTarget, file), func(w io.Writer) (int, error) {
		return writeOdooZip(ctx, e.Restic, snap, w)
	})
	if err != nil {
		return "", err
	}
	e.logf("copy to %s: %s with %d filestore file(s)", t.Store, file, files)
	return file, nil
}

// deleteZips deletes the zips of the forgotten snapshots from the
// instance's folder of a zip store, and the partial uploads earlier copies
// left there when they were killed mid-upload (".<zip>.partial": the
// upload only removes its own on an error it sees, and a retry writes
// under a new name). Only the names this instance's backups get are
// deleted, so nothing else in the folder is ever touched; nor is keep, the
// zip just written, should a forgotten backup share its second.
func deleteZips(ctx context.Context, e *Env, t CopyTarget, folder string, forgotten []restic.Snapshot, keep string) error {
	want := map[string]bool{}
	for _, s := range forgotten {
		want[ZipName(e.Config.Name, s.Time)] = true
	}
	delete(want, keep)
	dir := joinTarget(t.ZipTarget, folder)
	out, err := rcloneRun(ctx, t.Rclone, "lsf", "--files-only", dir)
	if err != nil && len(want) == 0 {
		// Only partial uploads were looked for; the next copy looks again.
		e.logf("copy to %s: could not look for partial uploads: %v", t.Store, err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("list the zips to delete: %w", err)
	}
	var failed, failedPartial []string
	for _, name := range strings.Split(string(out), "\n") {
		partial := isPartialZip(e.Config.Name, name)
		if !want[name] && !partial {
			continue
		}
		if _, err := rcloneRun(ctx, t.Rclone, "deletefile", joinTarget(dir, name)); err != nil {
			if partial {
				failedPartial = append(failedPartial, name)
			} else {
				failed = append(failed, name)
			}
			continue
		}
		if partial {
			e.logf("copy to %s: deleted %s, left by an upload that was cut short", t.Store, name)
		} else {
			e.logf("copy to %s: deleted %s, whose backup retention forgot", t.Store, name)
		}
	}
	var errs []string
	if len(failed) > 0 {
		errs = append(errs, fmt.Sprintf("could not delete %s, whose backups retention forgot", strings.Join(failed, ", ")))
	}
	if len(failedPartial) > 0 {
		errs = append(errs, fmt.Sprintf("could not delete %s, left by uploads that were cut short", strings.Join(failedPartial, ", ")))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// isPartialZip reports whether name is the temporary name uploadZip gives a
// zip copy of database db while it uploads: "." + ZipName(db, t) + ".partial".
func isPartialZip(db, name string) bool {
	zip, ok := strings.CutPrefix(name, ".")
	if !ok {
		return false
	}
	if zip, ok = strings.CutSuffix(zip, extZip+".partial"); !ok {
		return false
	}
	stamp, ok := strings.CutPrefix(zip, db+"_")
	if !ok {
		return false
	}
	_, err := time.Parse(zipTimeFormat, stamp)
	return err == nil && len(stamp) == len(zipTimeFormat)
}
