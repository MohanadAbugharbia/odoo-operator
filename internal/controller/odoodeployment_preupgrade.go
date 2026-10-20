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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// ReasonPreUpgradeBackup: a backup was created before an upgrade.
const ReasonPreUpgradeBackup = "PreUpgradeBackup"

// preUpgradeFor finds the backup status.preUpgradeBackup names, also when
// the cache has not seen it yet. It is nil when there is none (never taken,
// or deleted to take it again).
func (r *OdooDeploymentReconciler) preUpgradeFor(ctx context.Context, od *odoov1.OdooDeployment, w instanceWork) (*odoov1.OdooBackup, error) {
	name := od.Status.PreUpgradeBackup.Name
	if name == "" {
		return nil, nil
	}
	for i := range w.backups {
		if w.backups[i].Name == name {
			return &w.backups[i], nil
		}
	}
	b := &odoov1.OdooBackup{}
	err := r.reader().Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: name}, b)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// preUpgradeBackup holds the upgrade spec asks for back until the backup
// spec.backup.beforeUpgrade wants before it has ended. It reports whether the
// upgrade may start now; until then the running image keeps serving, except
// while the backup itself runs (with Odoo stopped).
func (r *OdooDeploymentReconciler) preUpgradeBackup(ctx context.Context, s *reconcileState, w instanceWork, configHash string) (ctrl.Result, bool, error) {
	od := s.od
	mode := od.Spec.Backup.BeforeUpgradeValue()
	if mode == odoov1.BeforeUpgradeOff {
		return ctrl.Result{}, true, nil
	}
	target := od.UpgradeTarget()
	if od.Status.PreUpgradeBackup.Target != target {
		od.Status.PreUpgradeBackup = odoov1.PreUpgradeBackupStatus{}
	}
	b, err := r.preUpgradeFor(ctx, od, w)
	if err != nil {
		res, err := s.fail(odoov1.ReasonReconcileFailed, err)
		return res, false, err
	}
	if b == nil {
		return r.createPreUpgradeBackup(ctx, s, target)
	}
	switch {
	case !b.DeletionTimestamp.IsZero():
		// Deleted to be taken again: a new one once it is gone.
		res, err := r.reconcileSteadyState(ctx, s, configHash, "the upgrade waits for backup "+b.Name+" to be deleted")
		return res, false, err
	case restorable(b.Status.Phase):
		return ctrl.Result{}, true, nil
	case b.Status.Phase == odoov1.BackupPhaseFailed && mode == odoov1.BeforeUpgradeBestEffort:
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonPreUpgradeBackupFailed, fmt.Sprintf(
			"upgrading to %s without a backup: backup %s failed (beforeUpgrade is BestEffort)", od.Spec.Image, b.Name))
		return ctrl.Result{}, true, nil
	case b.Status.Phase == odoov1.BackupPhaseFailed:
		msg := fmt.Sprintf("backup %s, taken before upgrading to %s, failed, so %s keeps running; "+
			"delete OdooBackup %s to try again: %s", b.Name, od.Spec.Image, od.Status.AppliedImage, b.Name, b.Status.Message)
		s.degraded(odoov1.ReasonPreUpgradeBackupFailed, msg)
		res, err := r.reconcileSteadyState(ctx, s, configHash, msg)
		return res, false, err
	}
	next, wait := w.nextBackup(r.now())
	if next != nil {
		res, err := r.startBackup(ctx, s, w, next, configHash)
		return res, false, err
	}
	// The backup waits to be retried; the old image serves meanwhile.
	res, err := r.reconcileSteadyState(ctx, s, configHash, "the upgrade waits for backup "+b.Name)
	if wait > 0 && (res.RequeueAfter == 0 || wait < res.RequeueAfter) {
		res.RequeueAfter = wait
	}
	return res, false, err
}

func (r *OdooDeploymentReconciler) createPreUpgradeBackup(ctx context.Context, s *reconcileState, target string) (ctrl.Result, bool, error) {
	od := s.od
	b := &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      preUpgradeBackupName(od.Name, r.now().UTC()),
			Namespace: od.Namespace,
			Labels: map[string]string{
				odoov1.LabelBackupKind:     odoov1.BackupKindPreUpgrade,
				odoov1.LabelOdooDeployment: od.Name,
			},
			Annotations: map[string]string{odoov1.AnnotationUpgradeTarget: target},
		},
		Spec: odoov1.OdooBackupSpec{OdooDeployment: od.Name},
	}
	if err := r.Create(ctx, b); err != nil {
		res, err := s.fail(odoov1.ReasonReconcileFailed, fmt.Errorf("create pre-upgrade OdooBackup %s: %w", b.Name, err))
		return res, false, err
	}
	od.Status.PreUpgradeBackup = odoov1.PreUpgradeBackupStatus{Name: b.Name, Target: target}
	r.event(od, corev1.EventTypeNormal, ReasonPreUpgradeBackup,
		fmt.Sprintf("Created backup %s before upgrading to %s", b.Name, od.Spec.Image))
	log.FromContext(ctx).Info("Created pre-upgrade backup", "backup", b.Name, "image", od.Spec.Image)
	return ctrl.Result{RequeueAfter: time.Second}, false, nil
}

// preUpgradeBackupName is <instance>-pre-upgrade-<yyyymmdd-hhmmss> (UTC),
// cut to 63 characters like scheduled backup names.
func preUpgradeBackupName(instance string, now time.Time) string {
	suffix := "-pre-upgrade-" + now.Format("20060102-150405")
	if max := 63 - len(suffix); len(instance) > max {
		instance = strings.TrimRight(instance[:max], "-.")
	}
	return instance + suffix
}
