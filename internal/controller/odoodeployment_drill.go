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
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
)

// Reasons of restore drill events.
const (
	ReasonDrillScheduled = "DrillScheduled"
	ReasonDrillRunning   = "DrillRunning"
	ReasonDrillSucceeded = "DrillSucceeded"
	ReasonDrillFailed    = "DrillFailed"
)

// restorable reports a backup phase a restore (and a drill) can read.
func restorable(phase string) bool {
	switch phase {
	case odoov1.BackupPhaseCompleted, odoov1.BackupPhaseCompletedWithWarnings, odoov1.BackupPhasePartiallyCompleted:
		return true
	}
	return false
}

// newestRestorable is the instance's newest backup a drill can restore.
func (w instanceWork) newestRestorable() *odoov1.OdooBackup {
	for i := len(w.backups) - 1; i >= 0; i-- {
		b := &w.backups[i]
		if restorable(b.Status.Phase) && b.Status.SnapshotID != "" && b.DeletionTimestamp.IsZero() {
			return b
		}
	}
	return nil
}

// startDrill claims the instance for the due restore drill. Odoo keeps
// running: the drill works on a scratch database and an emptyDir.
func (r *OdooDeploymentReconciler) startDrill(ctx context.Context, s *reconcileState, w instanceWork) (ctrl.Result, error) {
	od := s.od
	now := metav1.NewTime(r.now())
	b := w.newestRestorable()
	if b == nil {
		r.recordDrill(s, &odoov1.OdooDrillResult{
			Succeeded: false, Message: "there is no finished backup to restore", StartedAt: &now, CompletedAt: &now,
		})
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	od.Status.CurrentOperation = odoov1.OperationStatus{Kind: odoov1.OperationDrill, Name: b.Name, StartedAt: &now}
	log.FromContext(ctx).Info("Starting restore drill", "backup", b.Name)
	result, _, err := r.driveDrill(ctx, s, w)
	return result, err
}

// driveDrill moves the drill in status.currentOperation along.
func (r *OdooDeploymentReconciler) driveDrill(ctx context.Context, s *reconcileState, w instanceWork) (ctrl.Result, bool, error) {
	od := s.od
	op := &od.Status.CurrentOperation
	started := op.StartedAt
	if started == nil {
		now := metav1.NewTime(r.now())
		started, op.StartedAt = &now, &now
	}
	outcome := &odoov1.OdooDrillResult{Backup: op.Name, StartedAt: started}
	b := w.backup(op.Name)
	if b == nil && op.Job == "" {
		outcome.Message = "OdooBackup " + op.Name + " was deleted before the drill started"
		return r.finishDrill(ctx, s, outcome)
	}
	if b != nil {
		outcome.SnapshotID = b.Status.SnapshotID
	}

	if op.Job == "" {
		// A drill checks a restic snapshot: a zip copy is not one.
		src, refusal, err := r.backupSource(ctx, od, b, false, func(msg string) {
			r.event(od, corev1.EventTypeWarning, ReasonRestoreFromCopy, "The drill restores the copy in store "+msg)
		})
		if err != nil {
			return ctrl.Result{}, false, err
		}
		if refusal != "" {
			outcome.Message = refusal
			return r.finishDrill(ctx, s, outcome)
		}
		params, err := r.jobParams(ctx, od, src.store)
		if err != nil {
			s.degraded(ReasonBackupImageUnknown, err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, false, nil
		}
		image := b.Status.Image
		if image == "" {
			image = od.Status.AppliedImage
		}
		job, err := jobspec.DrillJob(params, jobspec.Drill{
			Suffix: layout.Suffix(started.Time), Snapshot: src.snapshot, Backup: b.Name,
			OdooImage: image, ExpectModules: od.Spec.Modules,
		})
		if err != nil {
			outcome.Message = err.Error()
			return r.finishDrill(ctx, s, outcome)
		}
		name, err := r.startOperationJob(ctx, s, &job)
		if err != nil {
			res, err := s.fail(odoov1.ReasonJobCreationFailed, err)
			return res, false, err
		}
		op.Job = name
		if name != job.Name {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
		}
		r.event(od, corev1.EventTypeNormal, ReasonDrillRunning, fmt.Sprintf(
			"job %s restores backup %s (snapshot %s) into scratch space", job.Name, b.Name, short(src.snapshot)))
	}

	out, err := r.observeJob(ctx, od.Namespace, op.Job)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	switch out.state {
	case reconcileloops.JobStateMissing:
		// The cache may not have the Job just created yet: only the API
		// server can say it is gone.
		err := r.reader().Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: op.Job}, &batchv1.Job{})
		if err == nil {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, false, nil
		}
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, false, err
		}
		outcome.Message = "job " + op.Job + " was deleted before it finished"
		return r.finishDrill(ctx, s, outcome)
	case reconcileloops.JobStateActive:
		if out.pullMessage != "" {
			s.degraded(ReasonDrillRunning, "drill job "+op.Job+" cannot start: "+out.pullMessage)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, false, nil
	}
	res := out.result
	if out.state == reconcileloops.JobStateSucceeded && out.decoded && res.OK {
		outcome.Succeeded = true
		outcome.Message = drillSummary(b, outcome.SnapshotID, res)
		if err := reconcileloops.DeleteMaintenanceJob(ctx, r.Client, out.job); err != nil {
			return ctrl.Result{}, false, err
		}
	} else {
		outcome.Job = op.Job
		outcome.Message = fmt.Sprintf("job %s %s; inspect with: kubectl -n %s logs job/%s --all-containers",
			op.Job, failureText(out), od.Namespace, op.Job)
	}
	return r.finishDrill(ctx, s, outcome)
}

// drillSummary sums up a passed drill.
func drillSummary(b *odoov1.OdooBackup, snapshot string, res result.Result) string {
	name := ""
	if b != nil {
		name = b.Name + " "
	}
	msg := fmt.Sprintf("backup %s(snapshot %s) restored, passed its checks and Odoo read it", name, short(snapshot))
	if len(res.MissingModules) > 0 {
		msg += "; it lacks modules " + strings.Join(res.MissingModules, ", ")
	}
	if len(res.Warnings) > 0 {
		msg += "; " + strings.Join(res.Warnings, "; ")
	}
	return msg
}

// finishDrill records the drill's outcome and frees the instance.
func (r *OdooDeploymentReconciler) finishDrill(ctx context.Context, s *reconcileState, outcome *odoov1.OdooDrillResult) (ctrl.Result, bool, error) {
	s.od.Status.CurrentOperation = odoov1.OperationStatus{}
	r.recordDrill(s, outcome)
	log.FromContext(ctx).Info("Restore drill finished", "backup", outcome.Backup, "succeeded", outcome.Succeeded)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

func (r *OdooDeploymentReconciler) recordDrill(s *reconcileState, outcome *odoov1.OdooDrillResult) {
	od := s.od
	if outcome.CompletedAt == nil {
		now := metav1.NewTime(r.now())
		outcome.CompletedAt = &now
	}
	od.Status.Backup.LastDrill = outcome
	od.Status.Backup.DrillDue = nil
	if outcome.Succeeded {
		od.Status.Backup.LastSuccessfulDrillTime = outcome.CompletedAt
		r.event(od, corev1.EventTypeNormal, ReasonDrillSucceeded, outcome.Message)
	} else {
		r.event(od, corev1.EventTypeWarning, ReasonDrillFailed, outcome.Message)
	}
}
