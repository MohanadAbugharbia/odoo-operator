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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// The bootstrap creates the instance's first OdooRestore, and deletes it
// with the instance.
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoorestores,verbs=create;delete

// ReasonBootstrap: the OdooRestore of spec.bootstrap.fromBackup was created.
const ReasonBootstrap = "Bootstrap"

// needsBootstrap: the instance starts from a backup and has not yet.
func needsBootstrap(od *odoov1.OdooDeployment) bool {
	return od.Spec.Bootstrap != nil && od.Spec.Bootstrap.FromBackup != nil &&
		len(od.Status.InitModulesInstalled) == 0 && !od.Status.Bootstrap.Completed
}

// bootstrapRestoreName is <instance>-bootstrap, cut to 63 characters.
func bootstrapRestoreName(instance string) string {
	const suffix = "-bootstrap"
	if max := 63 - len(suffix); len(instance) > max {
		instance = strings.TrimRight(instance[:max], "-.")
	}
	return instance + suffix
}

// bootstrap restores spec.bootstrap.fromBackup instead of initializing a
// new database: it creates the OdooRestore (again, after a failed one was
// deleted) and starts it. Nothing else runs until it completed, so Odoo
// never starts on an empty database. proceed is false while it is not done.
func (r *OdooDeploymentReconciler) bootstrap(ctx context.Context, s *reconcileState, w instanceWork,
	configHash string) (res ctrl.Result, proceed bool, err error) {
	od := s.od
	name := bootstrapRestoreName(od.Name)
	rs := &odoov1.OdooRestore{}
	err = r.Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: name}, rs)
	switch {
	case apierrors.IsNotFound(err):
		from := od.Spec.Bootstrap.FromBackup
		rs = &odoov1.OdooRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: od.Namespace,
				Labels:    map[string]string{odoov1.LabelOdooDeployment: od.Name},
			},
			Spec: odoov1.OdooRestoreSpec{
				OdooDeployment: od.Name,
				From:           from.BackupSource.DeepCopy(),
				Neutralize:     from.Neutralize,
				Upgrade:        from.Upgrade,
			},
		}
		// Garbage collected with the instance, so a new instance of the
		// same name never finds this one's (failed) bootstrap.
		if err := controllerutil.SetOwnerReference(od, rs, r.Scheme); err != nil {
			res, err := s.fail(odoov1.ReasonReconcileFailed, err)
			return res, false, err
		}
		if err := r.Create(ctx, rs); err != nil {
			res, err := s.fail(odoov1.ReasonReconcileFailed, fmt.Errorf("create bootstrap OdooRestore %s: %w", name, err))
			return res, false, err
		}
		od.Status.Bootstrap = odoov1.BootstrapStatus{Restore: name}
		od.Status.Phase = odoov1.PhaseInitializing
		r.event(od, corev1.EventTypeNormal, ReasonBootstrap, "Created OdooRestore "+name+" to start from the backup")
		log.FromContext(ctx).Info("Created bootstrap restore", "restore", name)
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case err != nil:
		res, err := s.fail(odoov1.ReasonReconcileFailed, err)
		return res, false, err
	case rs.Spec.OdooDeployment != od.Name:
		msg := fmt.Sprintf("OdooRestore %s restores %s, not %s; rename one of them", name, rs.Spec.OdooDeployment, od.Name)
		s.degraded(odoov1.ReasonBootstrapFailed, msg)
		return ctrl.Result{RequeueAfter: time.Minute}, false, nil
	}
	od.Status.Bootstrap.Restore = name
	switch rs.Status.Phase {
	case odoov1.RestorePhaseCompleted:
		// finishRestore marked the instance initialized.
		od.Status.Bootstrap.Completed = true
		return ctrl.Result{}, true, nil
	case odoov1.RestorePhaseFailed:
		msg := fmt.Sprintf("OdooRestore %s failed: %s; fix the cause and delete it to try again", name, rs.Status.Message)
		od.Status.Phase = odoov1.PhaseInitializing
		s.degraded(odoov1.ReasonBootstrapFailed, msg)
		s.ready(metav1.ConditionFalse, odoov1.ReasonBootstrapFailed, "the backup to start from was not restored")
		return ctrl.Result{RequeueAfter: time.Minute}, false, nil
	}
	if next := w.restore(name); next != nil && w.nextRestore() == next {
		res, err := r.startRestore(ctx, s, w, next, configHash)
		return res, false, err
	}
	// Not listed yet, or another restore of this instance goes first.
	od.Status.Phase = odoov1.PhaseInitializing
	s.ready(metav1.ConditionFalse, ReasonWaitingForOperation, "waiting to restore the backup to start from")
	return ctrl.Result{RequeueAfter: 5 * time.Second}, false, nil
}

// deleteBootstrapRestore deletes the instance's bootstrap OdooRestore when
// the instance is deleted. The owner reference does the same, but restores
// created before it was set have none.
func (r *OdooDeploymentReconciler) deleteBootstrapRestore(ctx context.Context, od *odoov1.OdooDeployment) error {
	name := od.Status.Bootstrap.Restore
	if name == "" {
		name = bootstrapRestoreName(od.Name)
	}
	rs := &odoov1.OdooRestore{}
	err := r.reader().Get(ctx, client.ObjectKey{Namespace: od.Namespace, Name: name}, rs)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get bootstrap OdooRestore %s: %w", name, err)
	}
	if rs.Spec.OdooDeployment != od.Name || !rs.DeletionTimestamp.IsZero() {
		return nil
	}
	if err := r.Delete(ctx, rs); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete bootstrap OdooRestore %s: %w", name, err)
	}
	log.FromContext(ctx).Info("Deleted bootstrap restore", "restore", name)
	return nil
}

// dropRestoreLeftovers drops, before the instance's own database, what its
// restores left on the server and have not cleaned up yet: the database a
// restore replaced (kept for an undo) and a half-restored copy. Each is
// dropped only when it carries this instance's owner tag; one that does not
// is left in place with a warning, as for the live database. done is false
// when a drop failed and the deletion has to wait.
func (r *OdooDeploymentReconciler) dropRestoreLeftovers(ctx context.Context, s *reconcileState,
	conn database.ConnInfo, live string) (done bool, err error) {
	od := s.od
	w, err := r.listWork(ctx, od)
	if err != nil {
		return false, err
	}
	tag := database.NewOwnerTag(od.Namespace, od.Name)
	for _, rs := range w.restores {
		st := rs.Status
		if st.Suffix == "" || st.CleanedUp {
			continue
		}
		names := []string{layout.PrevName(live, st.Suffix), layout.RestoreName(live, st.Suffix)}
		if st.Previous.Database != "" && st.Previous.Database != names[0] {
			names = append(names, st.Previous.Database)
		}
		for _, name := range names {
			if name == live {
				continue
			}
			err := r.DB.Drop(ctx, conn, name, tag)
			switch {
			case errors.Is(err, database.ErrNotOwned):
				msg := fmt.Sprintf("refusing to drop database %q left by OdooRestore %s: %v", name, rs.Name, err)
				log.FromContext(ctx).Info(msg)
				r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropRefused, msg)
			case err != nil:
				msg := fmt.Sprintf("could not drop database %q left by OdooRestore %s: %v", name, rs.Name, err)
				log.FromContext(ctx).Error(err, "database drop failed", "database", name)
				s.degraded(odoov1.ReasonDatabaseDropFailed, msg)
				r.event(od, corev1.EventTypeWarning, odoov1.ReasonDatabaseDropFailed, msg)
				return false, nil
			}
		}
	}
	return true, nil
}
