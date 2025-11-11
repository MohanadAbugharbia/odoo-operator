package reconcileloops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

type OdooAdminPasswordSecretReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

func (r *OdooAdminPasswordSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (corev1.Secret, error) {
	logger := log.FromContext(ctx)

	logger.V(1).Info("Reconciling Odoo Admin Password Secret")

	// Check if odoo admin password secret already exists, if not create a new one
	adminSecret := corev1.Secret{}
	adminSecretNamespacedName := r.OdooDeployment.CreateOdooAdminPasswordSecretNamespacedName()
	createAdminSecret := false
	err := r.Get(ctx, adminSecretNamespacedName, &adminSecret)
	if err != nil && errors.IsNotFound(err) {
		createAdminSecret = true
	} else if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s secret.", adminSecretNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooAdminSecretNotAvailable, fmt.Sprintf("error creating %s secret: %v", adminSecretNamespacedName.Name, err), metav1.ConditionFalse)
		return adminSecret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	newAdminSecret, err := r.OdooDeployment.Spec.Config.GetOdooAdminPasswordSecretTemplate(
		adminSecretNamespacedName.Name,
		adminSecretNamespacedName.Namespace,
	)
	if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s secret.", adminSecretNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooAdminSecretCreationFailed, fmt.Sprintf("error creating %s secret: %v", adminSecretNamespacedName.Name, err), metav1.ConditionFalse)
		return adminSecret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	ctrl.SetControllerReference(r.OdooDeployment, &adminSecret, r.Scheme)
	if createAdminSecret {
		logger.Info(fmt.Sprintf("Creating a new secret for %s", adminSecretNamespacedName.Name))
		adminSecret.Data = newAdminSecret.Data
		adminSecret.Name = adminSecretNamespacedName.Name
		adminSecret.Namespace = adminSecretNamespacedName.Namespace
		err = r.Create(ctx, &adminSecret)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s secret.", adminSecret.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooAdminSecretCreationFailed, fmt.Sprintf("error creating %s secret: %v", adminSecret.Name, err), metav1.ConditionFalse)
			return adminSecret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	} else if adminSecret.Data["password"] == nil {
		// Update only if there is no "password" key in the adminSecret
		logger.Info(fmt.Sprintf("Updating existing secret for %s", adminSecretNamespacedName.Name))
		adminSecret.Data = newAdminSecret.Data
		err = r.Update(ctx, &adminSecret)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error updating %s secret.", adminSecret.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooAdminSecretUpdateFailed, fmt.Sprintf("error updating %s secret: %v", adminSecret.Name, err), metav1.ConditionFalse)
			return adminSecret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}

	return adminSecret, nil
}
