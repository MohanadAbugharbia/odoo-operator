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
	"github.com/google/go-cmp/cmp"
)

type OdooConfigSecretReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
	AdminSecret    *corev1.Secret
}

func (r *OdooConfigSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (corev1.Secret, error) {
	logger := log.FromContext(ctx)

	logger.V(1).Info("Reconciling Odoo Config Secret")

	// Check if odoo config secret already exists, if not create a new one
	secret := corev1.Secret{}
	secretNamespacedName := r.OdooDeployment.CreateOdooConfigSecretNamespacedName()
	createSecret := false
	err := r.Get(ctx, secretNamespacedName, &secret)
	if err != nil && errors.IsNotFound(err) {
		createSecret = true
	} else if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s secret.", req.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", odoov1.ReasonOdooConfigSecretNotAvailable, fmt.Sprintf("error creating %s secret: %v", req.Name, err), metav1.ConditionFalse)
		return secret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	adminPassword, ok := r.AdminSecret.Data["password"]
	if !ok {
		logger.Error(err, fmt.Sprintf("error getting admin password for %s", req.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooAdminPasswordFailed, fmt.Sprintf("error getting admin password for %s from %s: %v", req.Name, r.AdminSecret.Name, err), metav1.ConditionFalse)
		return secret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	newSecret, err := r.OdooDeployment.CreateOdooConfigSecretObj(r.Client, ctx, string(adminPassword))
	if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s secret.", secretNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooConfigSecretCreationFailed, fmt.Sprintf("error creating %s secret: %v", secretNamespacedName.Name, err), metav1.ConditionFalse)
		return secret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	ctrl.SetControllerReference(r.OdooDeployment, &secret, r.Scheme)
	if createSecret {
		logger.Info(fmt.Sprintf("Creating a new secret for %s", secretNamespacedName.Name))
		secret.Data = newSecret.Data
		secret.Name = secretNamespacedName.Name
		secret.Namespace = secretNamespacedName.Namespace
		err = r.Create(ctx, &newSecret)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s secret.", secret.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", odoov1.ReasonOdooConfigSecretCreationFailed, fmt.Sprintf("error creating %s secret: %v", secret.Name, err), metav1.ConditionFalse)
			return secret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	} else if diff := cmp.Diff(secret.Data, newSecret.Data); diff != "" {
		logger.V(1).Info(fmt.Sprintf("Diff: %s", diff))
		logger.Info(fmt.Sprintf("Updating secret %s for %s", secretNamespacedName.Name, req.Name))
		secret.Data = newSecret.Data
		err = r.Update(ctx, &secret)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error updating %s secret.", secret.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonOdooConfigSecretUpdateFailed, fmt.Sprintf("error updating %s secret: %v", secret.Name, err), metav1.ConditionFalse)
			return secret, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}

	return secret, nil
}
