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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// backupRetention is the retention a backup Job applies, or nil when
// spec.backup.retention.keepAll turns it off. Backups marked keep, and the
// snapshots of restores still to run, are protected.
func backupRetention(od *odoov1.OdooDeployment, w instanceWork) *jobspec.Retention {
	spec := od.Spec.Backup.Retention
	if spec.KeepAll {
		return nil
	}
	tz := zoneOrUTC(od.Spec.Backup.TimeZone)
	ret := &jobspec.Retention{Spec: spec, TimeZone: tz}
	for i := range w.backups {
		b := &w.backups[i]
		if b.Spec.Keep && b.DeletionTimestamp.IsZero() {
			ret.ProtectBackups = append(ret.ProtectBackups, b.Name)
		}
	}
	for i := range w.restores {
		rs := &w.restores[i]
		switch rs.Status.Phase {
		case "", odoov1.RestorePhasePending, odoov1.RestorePhaseStopping, odoov1.RestorePhaseRestoring:
		default:
			continue
		}
		id := rs.Spec.SnapshotID
		if rs.Spec.Backup != "" {
			if b := w.backup(rs.Spec.Backup); b != nil {
				id = b.Status.SnapshotID
			}
		}
		if id != "" {
			ret.ProtectSnapshots = append(ret.ProtectSnapshots, id)
		}
	}
	return ret
}

// deleteForgotten deletes the OdooBackups whose snapshot in store retention
// forgot. The snapshot is gone from the repository, so the object would
// only point at nothing.
func (r *OdooDeploymentReconciler) deleteForgotten(ctx context.Context, od *odoov1.OdooDeployment, w instanceWork, store string, forgotten []string) error {
	if len(forgotten) == 0 {
		return nil
	}
	gone := map[string]bool{}
	for _, id := range forgotten {
		gone[id] = true
	}
	for i := range w.backups {
		b := &w.backups[i]
		if b.Status.Store != store || b.Status.SnapshotID == "" || !gone[b.Status.SnapshotID] || b.Spec.Keep {
			continue
		}
		if !b.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, b, client.Preconditions{UID: &b.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete expired OdooBackup %s: %w", b.Name, err)
		}
		msg := fmt.Sprintf("Deleted backup %s: retention forgot its snapshot %s", b.Name, short(b.Status.SnapshotID))
		r.event(od, corev1.EventTypeNormal, ReasonBackupExpired, msg)
		log.FromContext(ctx).Info("Deleted expired backup", "backup", b.Name, "snapshot", b.Status.SnapshotID)
	}
	return nil
}

// copyStores are the stores after the first in spec.backup.stores, the ones
// a backup is copied to. A store that cannot be used (missing, or a Secret it
// names is) is left out of the Job and reported as a failed copy.
func (r *OdooDeploymentReconciler) copyStores(ctx context.Context, od *odoov1.OdooDeployment) ([]*odoov1.OdooBackupStore, []odoov1.BackupCopy) {
	var stores []*odoov1.OdooBackupStore
	var skipped []odoov1.BackupCopy
	for _, name := range od.Spec.Backup.Stores[1:] {
		store, err := r.getWritableStore(ctx, od.Namespace, name)
		if err != nil {
			skipped = append(skipped, odoov1.BackupCopy{Store: name, Error: err.Error()})
			continue
		}
		stores = append(stores, store)
	}
	return stores, skipped
}

// mergeCopies is the copies of a backup: the stores the operator left out,
// then what the Job reported for the others, in spec order.
func mergeCopies(skipped []odoov1.BackupCopy, reported []result.Copy) []odoov1.BackupCopy {
	out := append([]odoov1.BackupCopy(nil), skipped...)
	for _, c := range reported {
		out = append(out, odoov1.BackupCopy{Store: c.Store, SnapshotID: c.SnapshotID, File: c.File, Error: c.Error})
	}
	return out
}
