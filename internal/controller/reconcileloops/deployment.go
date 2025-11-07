package reconcileloops

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"

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

type DeploymentReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (appsv1.Deployment, error) {
	logger := log.FromContext(ctx)

	// Check if the deployment already exists, if not create a new one
	deployment := appsv1.Deployment{}
	createDeployment := false
	err := r.Get(ctx, req.NamespacedName, &deployment)
	if err != nil && errors.IsNotFound(err) {
		// Create a new deployment for the OdooDeployment if it does not exist
		createDeployment = true
	} else if err != nil {
		// Error fetching deployment - requeue
		logger.Error(err, "Failed to get Deployment")
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", "FailedToGetDeployment", fmt.Sprintf("Failed to get Deployment: %v", err), metav1.ConditionFalse)
		return deployment, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	deploymentTemplate := r.OdooDeployment.GetDeploymentTemplate()

	if createDeployment {
		logger.Info(fmt.Sprintf("Creating a new Deployment for %s", req.Name))
		deployment = deploymentTemplate
		ctrl.SetControllerReference(r.OdooDeployment, &deployment, r.Scheme)
		err = r.Create(ctx, &deployment)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s deployment.", req.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", "DeploymentCreationFailed", fmt.Sprintf("error creating %s odoo deployment: %v", req.Name, err), metav1.ConditionFalse)
			return deployment, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	} else if diff := cmp.Diff(deployment.Spec, deploymentTemplate.Spec); diff != "" {
		logger.V(1).Info(fmt.Sprintf("Diff: %s", diff))
		logger.Info(fmt.Sprintf("Updating existing Deployment for %s", req.Name))
		deployment.Spec = deploymentTemplate.Spec
		ctrl.SetControllerReference(r.OdooDeployment, &deployment, r.Scheme)
		err = r.Update(ctx, &deployment)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error updating %s deployment.", req.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", "DeploymentUpdateFailed", fmt.Sprintf("error updating %s odoo deployment: %v", req.Name, err), metav1.ConditionFalse)
			return deployment, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}
	return deployment, nil
}
