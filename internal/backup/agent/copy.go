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
	"fmt"
	"os"

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
}

// CopyTargetsFromEnv builds the targets of a backup Job's --copy stores:
// the n-th store's repository and password file are in COPY<n>_RESTIC_REPOSITORY
// and COPY<n>_RESTIC_PASSWORD_FILE (jobspec.LocateCopy), and restic is
// otherwise run as for the instance's own store.
func CopyTargetsFromEnv(base *restic.Restic, stores []string) ([]CopyTarget, error) {
	out := make([]CopyTarget, 0, len(stores))
	for i, store := range stores {
		prefix := fmt.Sprintf("COPY%d_", i+1)
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

// copyAll copies the new snapshot to every target, one after the other.
func copyAll(ctx context.Context, e *Env, targets []CopyTarget, host, backup, id string, ret *Retention) []result.Copy {
	out := make([]result.Copy, 0, len(targets))
	for _, t := range targets {
		copyID, err := copySnapshot(ctx, e, t, host, backup, id, ret)
		c := result.Copy{Store: t.Store, SnapshotID: copyID}
		if err != nil {
			e.logf("copy to %s failed: %v", t.Store, err)
			c.Error = tail(err.Error())
		}
		out = append(out, c)
	}
	return out
}
