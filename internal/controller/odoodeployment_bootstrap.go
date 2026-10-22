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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// The bootstrap creates the instance's first OdooRestore.
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
	if err != nil && !apierrors.IsNotFound(err) {
		res, err := s.fail(odoov1.ReasonReconcileFailed, err)
		return res, false, err
	}
	if od.Status.Database.ProvisionedBy != odoov1.DatabaseProvisionedByOperator &&
		(err != nil || rs.Status.Phase != odoov1.RestorePhaseCompleted) {
		res, err := r.refuseBootstrap(ctx, s, rs, err == nil)
		return res, false, err
	}
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
		// Owned, not controlled: it goes with the instance whatever its
		// deletionPolicy, and the OdooRestore controller releases its
		// finalizer once the instance is gone.
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

// refuseBootstrap stops the bootstrap of an instance whose database the
// operator did not create: it existed before (status.database.provisionedBy
// external), so it holds someone's data, and a bootstrap only ever fills a
// new database. The instance waits, Odoo is neither started nor initialized
// on it, until spec.bootstrap is removed (Odoo then runs the existing
// database) or the database is replaced deliberately with an OdooRestore.
// A bootstrap OdooRestore that exists but never ran is failed, so that it
// does not run later as an ordinary restore. found says whether rs exists.
func (r *OdooDeploymentReconciler) refuseBootstrap(ctx context.Context, s *reconcileState,
	rs *odoov1.OdooRestore, found bool) (ctrl.Result, error) {
	od := s.od
	db := od.Status.Database.Name
	msg := fmt.Sprintf("database %q already exists and the operator did not create it, so spec.bootstrap.fromBackup is not restored over it: "+
		"a bootstrap only fills a new database. Remove spec.bootstrap to run Odoo on the existing database, "+
		"or create an OdooRestore to replace its data deliberately", db)
	if found && rs.Spec.OdooDeployment == od.Name && (rs.Status.Phase == "" || rs.Status.Phase == odoov1.RestorePhasePending) {
		if err := r.patchRestoreStatus(ctx, rs, func(st *odoov1.OdooRestoreStatus) {
			st.Phase = odoov1.RestorePhaseFailed
			st.Message = msg
			now := metav1.Now()
			st.CompletedAt = &now
			st.CleanedUp = true // nothing ran, nothing to clean
		}); err != nil {
			return s.fail(odoov1.ReasonReconcileFailed, err)
		}
	}
	od.Status.Phase = odoov1.PhasePending
	s.degraded(odoov1.ReasonBootstrapFailed, msg)
	s.ready(metav1.ConditionFalse, odoov1.ReasonBootstrapFailed, "the database to start from a backup already exists")
	if s.enteredDegraded(odoov1.ReasonBootstrapFailed) {
		r.event(od, corev1.EventTypeWarning, odoov1.ReasonBootstrapFailed, msg)
		log.FromContext(ctx).Info("Refused to bootstrap over an existing database", "database", db)
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}
