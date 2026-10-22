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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// While an instance is being deleted its backups and restores are settled
// here, not by the OdooBackup and OdooRestore reconcilers, which only take
// over once the instance is gone: the deletion waits for the instance's
// Jobs, so it is the one that knows how they ended.

// settleBackupOnDelete records the outcome of the backup the instance was
// taking when it was deleted, once its Job has finished: a snapshot the Job
// wrote is recorded on its OdooBackup like any other, and a failure is
// reported with the Job's reason. A Job that is gone, or has not finished,
// is left: the OdooBackup reconciler fails the backup once the instance is
// gone.
func (r *OdooDeploymentReconciler) settleBackupOnDelete(ctx context.Context, s *reconcileState, w instanceWork) error {
	op := s.od.Status.CurrentOperation
	if op.Kind != odoov1.OperationBackup || op.Job == "" {
		return nil
	}
	b := w.backup(op.Name)
	if b == nil {
		return nil
	}
	out, err := r.observeJob(ctx, s.od.Namespace, op.Job)
	if err != nil {
		return err
	}
	switch out.state {
	case reconcileloops.JobStateSucceeded, reconcileloops.JobStateFailed:
	default:
		return nil
	}
	phase := s.od.Status.Phase
	_, _, err = r.driveBackup(ctx, s, w, b, "")
	s.od.Status.Phase = phase
	return err
}

// dropRestoreData drops, with the instance's database, every database its
// restores made, which nothing would drop once the instance is gone: the
// data a restore replaced (<db>_prev_<suffix>, owner-tagged as the live
// database was, and for a bootstrap the empty database it replaced) and the
// copy a restore loads into (<db>_restore_<suffix>, which carries no owner
// comment and is dropped by its exact name). That covers a restore the
// deletion cut short, whose Job may have swapped the data in without the
// restore ever recording it. It also deletes the bootstrap OdooRestore,
// which would otherwise make a new instance of the same name find its old,
// finished bootstrap. retry is set when a drop failed and should be tried
// again.
func (r *OdooDeploymentReconciler) dropRestoreData(ctx context.Context, s *reconcileState, w instanceWork,
	conn database.ConnInfo, tag database.OwnerTag) (retry bool, err error) {
	od := s.od
	live := od.Status.Database.Name
	for i := range w.restores {
		rs := &w.restores[i]
		var dropped []string
		refused := false
		drop := func(name string, byName bool) bool {
			var err error
			if byName {
				err = r.DB.DropCopy(ctx, conn, live, name, tag)
			} else {
				err = r.DB.Drop(ctx, conn, name, tag)
			}
			switch {
			case errors.Is(err, database.ErrNotOwned), errors.Is(err, database.ErrNotCopy):
				refused = true
				r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropRefused,
					fmt.Sprintf("refusing to drop database %q of OdooRestore %s: %v", name, rs.Name, err))
			case err != nil:
				msg := fmt.Sprintf("could not drop database %q of OdooRestore %s: %v", name, rs.Name, err)
				s.degraded(odoov1.ReasonDatabaseDropFailed, msg)
				r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropFailed, msg)
				return false
			default:
				dropped = append(dropped, name)
			}
			return true
		}
		st := rs.Status
		if st.Previous.State == odoov1.PreviousKept && st.Previous.Database != "" {
			if !drop(st.Previous.Database, false) {
				return true, nil
			}
		}
		leftovers := st.Suffix != "" && !st.CleanedUp && layout.ValidSuffix(st.Suffix)
		if leftovers {
			if prev := layout.PrevName(live, st.Suffix); prev != st.Previous.Database && !drop(prev, false) {
				return true, nil
			}
			if !drop(layout.RestoreName(live, st.Suffix), true) {
				return true, nil
			}
		}
		for _, name := range dropped {
			r.event(od, corev1.EventTypeNormal, odoov1.ReasonDatabaseDropped,
				fmt.Sprintf("dropped database %q of OdooRestore %s", name, rs.Name))
		}
		running := st.Phase == odoov1.RestorePhaseStopping || st.Phase == odoov1.RestorePhaseRestoring
		err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
			if st.Previous.State == odoov1.PreviousKept && !refused {
				st.Previous.State = odoov1.PreviousRemoved
				st.Previous.Database, st.Previous.Filestore = "", ""
				st.Previous.KeptUntil = nil
			}
			if leftovers && !refused {
				st.CleanedUp = true
			}
			if running {
				st.Phase = odoov1.RestorePhaseFailed
				st.Message = fmt.Sprintf("OdooDeployment %s was deleted while the restore was running", od.Name)
				if st.Job != "" {
					st.Message += fmt.Sprintf("; its deletion waited for job %s to stop", st.Job)
				}
				st.Message += ", then dropped the databases the restore made with the instance's"
				now := metav1.Now()
				st.CompletedAt = &now
				st.Job = ""
				st.RetryAfter = nil
			}
		})
		if client.IgnoreNotFound(err) != nil {
			return false, err
		}
		if rs.Name == bootstrapRestoreName(od.Name) {
			if err := r.Delete(ctx, rs); client.IgnoreNotFound(err) != nil {
				return false, err
			}
		}
	}
	return false, nil
}

// releaseRestores drops the finalizer of every restore of the instance once
// its deletion has dealt with what they made: while the instance exists,
// being deleted or not, only its own reconciler lets a restore go.
func (r *OdooDeploymentReconciler) releaseRestores(ctx context.Context, w instanceWork) error {
	for i := range w.restores {
		if err := r.setRestoreFinalizer(ctx, &w.restores[i], false); err != nil {
			return err
		}
	}
	return nil
}
