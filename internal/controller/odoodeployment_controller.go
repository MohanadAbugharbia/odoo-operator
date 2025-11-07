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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/controller/reconcileloops"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// OdooDeploymentReconciler reconciles a OdooDeployment object
type OdooDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var apiSGVString = odoov1.GroupVersion.String()

// IsOwnedByOdooDeployment checks that an object is owned by a OdooDeployment and returns
// the owner name
func IsOwnedByOdooDeployment(obj client.Object) (string, bool) {
	owner := metav1.GetControllerOf(obj)
	if owner == nil {
		return "", false
	}

	if owner.Kind != odoov1.OdooDeploymentKind {
		return "", false
	}

	if owner.APIVersion != apiSGVString {
		return "", false
	}

	return owner.Name, true
}

// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=odoo.abugharbia.com,resources=odoodeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OdooDeployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *OdooDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the OdooDeployment instance
	odooDeployment := &odoov1.OdooDeployment{}
	err := r.Get(ctx, req.NamespacedName, odooDeployment)
	if err == nil {
		logger.Info("Current OdooDeployment state",
			"resourceVersion", odooDeployment.ResourceVersion,
			"generation", odooDeployment.Generation)
	}
	if err != nil && errors.IsNotFound(err) {
		logger.Info("OdooDeployment resource object not found.")
		return ctrl.Result{}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get OdooDeployment")
		utils.UpdateStatus(&odooDeployment.Status.Conditions, "OperatorDegraded", "FailedToGetOdooDeployment", fmt.Sprintf("Failed to get OdooDeployment: %v", err), metav1.ConditionFalse)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	logger.Info("Reconciling OdooDeployment",
		"resourceVersion", odooDeployment.ResourceVersion,
		"generation", odooDeployment.Generation)

	odooAdminSecretReconciler := reconcileloops.OdooAdminPasswordSecretReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	adminSecret, err := odooAdminSecretReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo admin password secret")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	odooDeployment.Status.OdooAdminSecretName = adminSecret.Name
	r.Status().Update(ctx, odooDeployment)

	odooConfigSecretReconciler := reconcileloops.OdooConfigSecretReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
		AdminSecret:    &adminSecret,
	}

	odooConfigSecret, err := odooConfigSecretReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo config secret")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	odooDeployment.Status.OdooConfigSecretName = odooConfigSecret.Name

	r.Status().Update(ctx, odooDeployment)

	odooFilestoreReconciler := reconcileloops.OdooFilestoreReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	pvc, err := odooFilestoreReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo data PVC")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	odooDeployment.Status.OdooDataPvcName = pvc.Name
	r.Status().Update(ctx, odooDeployment)

	odooDatabaseInitJobReconciler := reconcileloops.OdooDatabaseInitJobReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	result, err, requeue := odooDatabaseInitJobReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo database init job")
		return result, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	} else if requeue {
		return result, r.Status().Update(ctx, odooDeployment)
	}

	deploymentReconciler := reconcileloops.DeploymentReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	_, err = deploymentReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo deployment")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	r.Status().Update(ctx, odooDeployment)

	odooHttpServiceReconciler := reconcileloops.OdooHttpServiceReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	_, err = odooHttpServiceReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo HTTP service")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	odooPollServiceReconciler := reconcileloops.OdooPollServiceReconciler{
		Client:         r.Client,
		Scheme:         r.Scheme,
		OdooDeployment: odooDeployment,
	}

	_, err = odooPollServiceReconciler.Reconcile(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to reconcile Odoo poll service")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, odooDeployment)})
	}

	logger.Info("Finished reconciling OdooDeployment")

	utils.UpdateStatus(&odooDeployment.Status.Conditions, "OperatorSucceeded", "ReconcileSucceeded", "Reconcile succeeded", metav1.ConditionTrue)
	return ctrl.Result{}, utilerrors.NewAggregate([]error{nil, r.Status().Update(ctx, odooDeployment)})
}

// SetupWithManager sets up the controller with the Manager.
func (r *OdooDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&odoov1.OdooDeployment{}).
		Owns(&corev1.Secret{}, builder.WithPredicates(secretsPredicate)).
		Owns(&corev1.PersistentVolumeClaim{}, builder.WithPredicates(pvcPredicate)).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(deploymentPredicate)).
		Owns(&corev1.Service{}, builder.WithPredicates(servicePredicate)).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapSecretsToOdooDeployments()),
			builder.WithPredicates(secretsPredicate),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}

// mapSecretsToOdooDeployments returns a function mapping OdooDeployment events watched to OdooDeployment reconcile requests
func (r *OdooDeploymentReconciler) mapSecretsToOdooDeployments() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}
		odooDeployments, err := r.getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(ctx, secret)
		if err != nil {
			log.FromContext(ctx).Error(err, "while getting OdooDeployment list", "namespace", secret.Namespace)
			return nil
		}
		// build requests for OdooDeployment referring the secret
		return filterOdooDeploymentsUsingSecret(odooDeployments, secret)
	}
}

func (r *OdooDeploymentReconciler) getOdooDeploymentsForSecretsOrConfigMapsToOdooDeploymentsMapper(
	ctx context.Context,
	object metav1.Object,
) (odooDeployments odoov1.OdooDeploymentList, err error) {
	_, isSecret := object.(*corev1.Secret)
	_, isConfigMap := object.(*corev1.ConfigMap)

	if !isSecret && !isConfigMap {
		return odooDeployments, fmt.Errorf("unsupported object: %+v", object)
	}

	// Get all the Odoo Deployments handled by the operator in the secret namespaces
	err = r.List(
		ctx,
		&odooDeployments,
		client.InNamespace(object.GetNamespace()),
	)
	return odooDeployments, err
}

// filterOdooDeploymentsUsingSecret returns a list of reconcile.Request for the Odoo Deployments
// that reference the secret
func filterOdooDeploymentsUsingSecret(
	odooDeployments odoov1.OdooDeploymentList,
	secret *corev1.Secret,
) (requests []reconcile.Request) {
	for _, deployment := range odooDeployments.Items {
		if deployment.UsesSecret(secret.Name) {
			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      deployment.Name,
						Namespace: deployment.Namespace,
					},
				},
			)
			continue
		}
	}
	return requests
}
