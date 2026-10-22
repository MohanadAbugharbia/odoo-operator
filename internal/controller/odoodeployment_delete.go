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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// While an instance is being deleted its backups and restores are settled
// here, not by the OdooBackup and OdooRestore reconcilers, which only take
// over once the instance is gone: the deletion waits for the instance's
// Jobs, so it is the one that knows how they ended.

// dropWorkData deletes, before the instance's database is dropped, what its
// restores and drills made: on a retained data volume the restores' folders
// (through their cleanup Jobs), then the restores' databases and the
// drills' scratch databases. wait is how long to wait before
// trying again when that is not done.
func (r *OdooDeploymentReconciler) dropWorkData(ctx context.Context, s *reconcileState, w instanceWork,
	conn database.ConnInfo, tag database.OwnerTag) (wait time.Duration, err error) {
	filesStay, err := r.retainedVolume(ctx, s.od)
	if err != nil {
		return 0, err
	}
	if filesStay {
		done, err := r.cleanUpFilesOnDelete(ctx, s, w)
		if err != nil || !done {
			return 5 * time.Second, err
		}
	}
	if retry, err := r.dropRestoreData(ctx, s, w, conn, tag, filesStay); err != nil || retry {
		return 30 * time.Second, err
	}
	if retry := r.dropDrillDatabases(ctx, s, conn, tag); retry {
		return 30 * time.Second, nil
	}
	return 0, nil
}

// dropDrillDatabases drops the scratch databases restore drills of the
// instance left (<db>_drill_<suffix>, found by name, since they carry no
// owner comment): a drill pod that died before its last step leaves one,
// and only the instance's next drill would have dropped it. retry is set
// when a drop failed and should be tried again.
func (r *OdooDeploymentReconciler) dropDrillDatabases(ctx context.Context, s *reconcileState,
	conn database.ConnInfo, tag database.OwnerTag) (retry bool) {
	od := s.od
	live := od.Status.Database.Name
	names, err := r.DB.Databases(ctx, conn)
	if err != nil {
		msg := fmt.Sprintf("could not list the databases to drop the drills' scratch databases: %v", err)
		s.degraded(odoov1.ReasonDatabaseDropFailed, msg)
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropFailed, msg)
		return true
	}
	for _, name := range names {
		if layout.DrillSuffix(name, live) == "" {
			continue
		}
		err := r.DB.DropCopy(ctx, conn, live, name, tag)
		switch {
		case errors.Is(err, database.ErrNotOwned), errors.Is(err, database.ErrNotCopy):
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropRefused,
				fmt.Sprintf("refusing to drop drill database %q: %v", name, err))
		case err != nil:
			msg := fmt.Sprintf("could not drop drill database %q: %v", name, err)
			s.degraded(odoov1.ReasonDatabaseDropFailed, msg)
			r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropFailed, msg)
			return true
		default:
			r.event(od, corev1.EventTypeNormal, odoov1.ReasonDatabaseDropped, fmt.Sprintf("dropped drill database %q", name))
		}
	}
	return false
}

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
// finished bootstrap. With filesStay (the data volume is retained), a
// restore whose folders the cleanup Job could not delete keeps them
// recorded: status.cleanedUp stays false and status.previous.filestore
// names its folder. retry is set when a drop failed and should be tried
// again.
func (r *OdooDeploymentReconciler) dropRestoreData(ctx context.Context, s *reconcileState, w instanceWork,
	conn database.ConnInfo, tag database.OwnerTag, filesStay bool) (retry bool, err error) {
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
			switch {
			case refused:
			case leftovers && filesStay:
				// The database is gone, the folders are not.
				st.Previous.Database = ""
			default:
				if st.Previous.State == odoov1.PreviousKept {
					st.Previous.State = odoov1.PreviousRemoved
					st.Previous.Database, st.Previous.Filestore = "", ""
					st.Previous.KeptUntil = nil
				}
				if leftovers {
					st.CleanedUp = true
				}
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

// AnnotationFilesKept marks a restore whose leftovers on a retained data
// volume the instance's deletion could not delete; its value says why. The
// deletion does not try that restore's cleanup again.
const AnnotationFilesKept = "odoo.abugharbia.com/files-kept"

// annotationStartedByDeletion marks the cleanup Jobs an instance's deletion
// starts, which it waits for even before their pod starts.
const annotationStartedByDeletion = "odoo.abugharbia.com/started-by-deletion"

// ReasonFilesKept: a restore's folders stay on the retained data volume.
const ReasonFilesKept = "RestoreFilesKept"

// retainedVolume reports whether the instance's data volume outlives it: it
// exists, is not being deleted, and is not owned by the instance (with
// spec.odooFilestore.deletionPolicy Retain, the default). What restores left
// on it would otherwise stay there.
func (r *OdooDeploymentReconciler) retainedVolume(ctx context.Context, od *odoov1.OdooDeployment) (bool, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.reader().Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: od.GetPvcName()}, pvc)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get the data volume claim: %w", err)
	}
	return pvc.DeletionTimestamp.IsZero() && !metav1.IsControlledBy(pvc, od), nil
}

// needsFileCleanup reports whether restore rs left folders, a staging dir or
// a journal on the data volume that the deletion still has to delete.
func needsFileCleanup(rs *odoov1.OdooRestore) bool {
	return layout.ValidSuffix(rs.Status.Suffix) && !rs.Status.CleanedUp && rs.Annotations[AnnotationFilesKept] == ""
}

// cleanUpFilesOnDelete runs, before a retained data volume is left behind,
// the cleanup Job of every restore of the instance that left something on
// it, one at a time (the instance's pods are stopped, so the Job mounts the
// volume alone): it deletes <db>_prev_<suffix> and <db>_restore_<suffix>
// (folders and databases), the staging folder and the journal, exactly as a
// cleanup after keepPrevious does. A Job that fails, or cannot be created
// because the namespace is being deleted, is not retried: the restore is
// marked with AnnotationFilesKept and its folders stay, recorded in its
// status. done is false while a Job runs.
func (r *OdooDeploymentReconciler) cleanUpFilesOnDelete(ctx context.Context, s *reconcileState, w instanceWork) (done bool, err error) {
	od := s.od
	op := &od.Status.CurrentOperation
	if op.Kind == odoov1.OperationCleanup && op.Job != "" {
		rs := w.restore(op.Name)
		out, err := r.observeJob(ctx, od.Namespace, op.Job)
		if err != nil {
			return false, err
		}
		switch {
		case out.state == reconcileloops.JobStateActive:
			return false, nil
		case rs == nil || out.state == reconcileloops.JobStateMissing:
		case out.state == reconcileloops.JobStateSucceeded && out.decoded && out.result.OK:
			err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
				st.CleanedUp = true
				st.RetryAfter = nil
				if st.Previous.State == odoov1.PreviousKept {
					st.Previous.State = odoov1.PreviousRemoved
					st.Previous.KeptUntil = nil
				}
				st.Previous.Database, st.Previous.Filestore = "", ""
			})
			if err != nil {
				return false, err
			}
			if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
				return false, err
			}
		default:
			if err := r.keepFiles(ctx, s, rs, fmt.Sprintf("cleanup job %s %s", op.Job, failureText(out))); err != nil {
				return false, err
			}
		}
		*op = odoov1.OperationStatus{}
	}
	for i := range w.restores {
		rs := &w.restores[i]
		if !needsFileCleanup(rs) {
			continue
		}
		params, err := r.jobParams(ctx, od, nil)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return false, nil
		}
		attempt := rs.Status.CleanupAttempts + 1
		job := jobspec.CleanupJob(params, rs, rs.Status.Suffix, attempt)
		metav1.SetMetaDataAnnotation(&job.ObjectMeta, annotationStartedByDeletion, "true")
		if err := r.createJob(ctx, od, &job); err != nil {
			if apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
				// Nothing can run any more; the volume goes with the namespace.
				return true, r.keepFiles(ctx, s, rs, "the namespace is being deleted")
			}
			return false, err
		}
		if err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) { st.CleanupAttempts = attempt }); err != nil {
			return false, err
		}
		now := metav1.Now()
		*op = odoov1.OperationStatus{Kind: odoov1.OperationCleanup, Name: rs.Name, Job: job.Name, StartedAt: &now}
		s.ready(metav1.ConditionFalse, odoov1.ReasonDeleting,
			fmt.Sprintf("OdooDeployment is being deleted; job %s deletes what OdooRestore %s left on the data volume", job.Name, rs.Name))
		log.FromContext(ctx).Info("Cleaning up a restore's files before the data volume is left behind", "restore", rs.Name, "job", job.Name)
		return false, nil
	}
	return true, nil
}

// keepFiles gives up deleting what restore rs left on the retained data
// volume, saying where it is.
func (r *OdooDeploymentReconciler) keepFiles(ctx context.Context, s *reconcileState, rs *odoov1.OdooRestore, why string) error {
	od := s.od
	live, suffix := od.Status.Database.Name, rs.Status.Suffix
	paths := []string{
		layout.FilestorePath(layout.PrevName(live, suffix)), layout.FilestorePath(layout.RestoreName(live, suffix)),
		layout.StagingDir(suffix), layout.JournalFile(suffix),
	}
	msg := fmt.Sprintf("%s; what OdooRestore %s left stays on volume %s (any of %s): delete it by hand",
		why, rs.Name, od.GetPvcName(), strings.Join(paths, ", "))
	patched := rs.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}
	patched.Annotations[AnnotationFilesKept] = msg
	if err := r.Patch(ctx, patched, client.MergeFrom(rs)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("patch OdooRestore %s: %w", rs.Name, err)
	}
	rs.ObjectMeta = patched.ObjectMeta
	r.event(od, corev1.EventTypeWarning, ReasonFilesKept, msg)
	r.objectEvent(rs, corev1.EventTypeWarning, ReasonFilesKept, msg)
	return nil
}
