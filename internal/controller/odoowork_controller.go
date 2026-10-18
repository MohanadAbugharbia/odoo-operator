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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// The OdooDeployment reconciler runs backups and restores. The two small
// reconcilers here only give a new object a phase straight away and settle
// the ones whose OdooDeployment does not exist, which no OdooDeployment
// reconcile would ever see.

// OdooBackupReconciler marks new backups Pending and fails the ones whose
// instance is gone.
type OdooBackupReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoobackups/status,verbs=get;update;patch

// Reconcile gives the backup a phase.
func (r *OdooBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	b := &odoov1.OdooBackup{}
	if err := r.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	found, err := odooDeploymentExists(ctx, r.Client, b.Namespace, b.Spec.OdooDeployment)
	if err != nil {
		return ctrl.Result{}, err
	}
	base := b.DeepCopy()
	switch {
	case b.Status.Phase == "" && found:
		b.Status.Phase = odoov1.BackupPhasePending
		b.Status.Message = "queued on OdooDeployment " + b.Spec.OdooDeployment
	case (b.Status.Phase == "" || b.Status.Phase == odoov1.BackupPhasePending) && !found:
		b.Status.Phase = odoov1.BackupPhasePending
		b.Status.Message = fmt.Sprintf("waiting for OdooDeployment %s, which does not exist", b.Spec.OdooDeployment)
	case !found && (b.Status.Phase == odoov1.BackupPhaseRunning || b.Status.Phase == odoov1.BackupPhaseWaitingForFilestoreCleanup):
		b.Status.Phase = odoov1.BackupPhaseFailed
		b.Status.Message = fmt.Sprintf("OdooDeployment %s was deleted while the backup was running", b.Spec.OdooDeployment)
		b.Status.Job = ""
		b.Status.RetryAfter = nil
	default:
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Status().Patch(ctx, b, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdooBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odoov1.OdooBackup{}).
		Watches(&odoov1.OdooDeployment{}, handler.EnqueueRequestsFromMapFunc(r.backupsOf)).
		Named("odoobackup").
		Complete(r)
}

// OdooRestoreReconciler marks new restores Pending, fails the ones whose
// instance is gone and releases their finalizer: with the instance gone there
// is nothing left to clean up.
type OdooRestoreReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores/finalizers,verbs=update

// Reconcile gives the restore a phase.
func (r *OdooRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rs := &odoov1.OdooRestore{}
	if err := r.Get(ctx, req.NamespacedName, rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	found, err := odooDeploymentExists(ctx, r.Client, rs.Namespace, rs.Spec.OdooDeployment)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found && controllerutil.ContainsFinalizer(rs, odoov1.RestoreFinalizer) &&
		(!rs.DeletionTimestamp.IsZero() || rs.Status.Phase == odoov1.RestorePhaseCompleted || rs.Status.Phase == odoov1.RestorePhaseFailed) {
		patched := rs.DeepCopy()
		controllerutil.RemoveFinalizer(patched, odoov1.RestoreFinalizer)
		if err := r.Patch(ctx, patched, client.MergeFromWithOptions(rs, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}
	if !rs.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := rs.DeepCopy()
	switch {
	case rs.Status.Phase == "" && found:
		rs.Status.Phase = odoov1.RestorePhasePending
		rs.Status.Message = "queued on OdooDeployment " + rs.Spec.OdooDeployment
	case (rs.Status.Phase == "" || rs.Status.Phase == odoov1.RestorePhasePending) && !found:
		rs.Status.Phase = odoov1.RestorePhasePending
		rs.Status.Message = fmt.Sprintf("waiting for OdooDeployment %s, which does not exist", rs.Spec.OdooDeployment)
	case !found && (rs.Status.Phase == odoov1.RestorePhaseStopping || rs.Status.Phase == odoov1.RestorePhaseRestoring):
		rs.Status.Phase = odoov1.RestorePhaseFailed
		rs.Status.Message = fmt.Sprintf("OdooDeployment %s was deleted while the restore was running", rs.Spec.OdooDeployment)
		rs.Status.Job = ""
		rs.Status.RetryAfter = nil
	default:
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Status().Patch(ctx, rs, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdooRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odoov1.OdooRestore{}).
		Watches(&odoov1.OdooDeployment{}, handler.EnqueueRequestsFromMapFunc(r.restoresOf)).
		Named("odoorestore").
		Complete(r)
}

// backupsOf maps an OdooDeployment to its backups that are not finished.
func (r *OdooBackupReconciler) backupsOf(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &odoov1.OdooBackupList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.OdooDeployment == obj.GetName() && !backupFinished(b.Status.Phase) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(b)})
		}
	}
	return reqs
}

func backupFinished(phase string) bool {
	switch phase {
	case odoov1.BackupPhaseCompleted, odoov1.BackupPhaseCompletedWithWarnings, odoov1.BackupPhasePartiallyCompleted, odoov1.BackupPhaseFailed:
		return true
	}
	return false
}

// restoresOf maps an OdooDeployment to its restores that still hold a
// finalizer or are not finished.
func (r *OdooRestoreReconciler) restoresOf(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &odoov1.OdooRestoreList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		rs := &list.Items[i]
		if rs.Spec.OdooDeployment == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(rs)})
		}
	}
	return reqs
}

// odooDeploymentExists reports whether the instance exists and is not being deleted.
func odooDeploymentExists(ctx context.Context, c client.Reader, namespace, name string) (bool, error) {
	od := &odoov1.OdooDeployment{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, od)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return od.DeletionTimestamp.IsZero(), nil
}
