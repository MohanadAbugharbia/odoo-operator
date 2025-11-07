package reconcileloops

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

type OdooDatabaseInitJobReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

// Reconcile handles the reconciliation of the OdooDatabaseInitJob
// Returns ctrl.Result, error, bool (indicating whether to requeue)
func (r *OdooDatabaseInitJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error, bool) {
	logger := log.FromContext(ctx)
	// Check if there is a current init job running
	if r.OdooDeployment.Status.CurrentInitJob.Name != "" && r.OdooDeployment.Status.CurrentInitJob.Namespace != "" {
		// Get the Job details
		logger.Info("Current InitJob defined, checking status")
		// Get the status of the current init job
		currentInitJob := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      r.OdooDeployment.Status.CurrentInitJob.Name,
			Namespace: r.OdooDeployment.Status.CurrentInitJob.Namespace,
		}, currentInitJob)
		if err != nil && errors.IsNotFound(err) {
			logger.Info("Current InitJob not found")
		} else if err != nil {
			logger.Error(err, "Failed to get current InitJob")
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", "FailedToGetInitJob", fmt.Sprintf("Failed to get current InitJob: %v", err), metav1.ConditionFalse)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)}), true
		}

		// If job succeeded, update the status of the OdooDeployment
		if currentInitJob.Status.Succeeded > 0 {
			// The current init job has succeeded, so we can clear it
			logger.Info("Current InitJob succeeded, clearing")
			r.OdooDeployment.Status.CurrentInitJob.Name = ""
			r.OdooDeployment.Status.CurrentInitJob.Namespace = ""
			r.OdooDeployment.Status.InitModulesInstalled = r.OdooDeployment.Status.CurrentInitJob.Modules
			r.OdooDeployment.Status.CurrentInitJob.Modules = []string{}
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", "InitJobSucceeded", "InitJob succeeded, clearing", metav1.ConditionTrue)

			// Search for the pods with the label job-name = currentInitJob.Name
			pods := &corev1.PodList{}
			err = r.List(ctx, pods, client.MatchingLabels{"job-name": currentInitJob.Name})
			if err != nil {
				logger.Error(err, "Failed to list pods")
				utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", "FailedToListPods", fmt.Sprintf("Failed to list pods: %v", err), metav1.ConditionFalse)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)}), true
			}
			for _, pod := range pods.Items {
				// Delete the pod
				logger.Info(fmt.Sprintf("Deleting pod %s from job %s", pod.Name, currentInitJob.Name))
				err = r.Delete(ctx, &pod)
				if err != nil {
					logger.Error(err, "Failed to delete pod")
					utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", "FailedToDeletePod", fmt.Sprintf("Failed to delete pod: %v", err), metav1.ConditionFalse)
					return ctrl.Result{RequeueAfter: 30 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)}), true
				}
			}

			// Now we delete the Job
			logger.Info(fmt.Sprintf("Deleting job %s", currentInitJob.Name))
			err = r.Delete(ctx, currentInitJob)
			if err != nil {
				logger.Error(err, "Failed to delete current InitJob")
				utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", "FailedToDeleteInitJob", fmt.Sprintf("Failed to delete current InitJob: %v", err), metav1.ConditionFalse)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)}), true
			}

			return ctrl.Result{RequeueAfter: 30 * time.Second}, utilerrors.NewAggregate([]error{nil, r.Status().Update(ctx, r.OdooDeployment)}), true
		} else if currentInitJob.Status.Active > 0 {
			// If job is still running, requeue
			logger.Info("Current InitJob still running, requening")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil, true
		}
		// If job failed, update the status of the OdooDeployment

	}
	// Check if InitModulesInstalled list matches the Spec.Modules list
	// If not, create a new InitJob to install the missing modules
	// If the list is empty, create a new InitJob to install all modules
	// If the list is the same, do nothing
	logger.V(1).Info(fmt.Sprintf("Currently installed modules: %d", len(r.OdooDeployment.Status.InitModulesInstalled)))
	if len(r.OdooDeployment.Status.InitModulesInstalled) == 0 || len(r.OdooDeployment.Status.InitModulesInstalled) != len(r.OdooDeployment.Spec.Modules) {
		// Create a new InitJob to install all modules
		logger.Info("Creating a new InitJob to install modules")

		initJob, modulesToInstall := r.OdooDeployment.GetDbInitJobTemplate()
		ctrl.SetControllerReference(r.OdooDeployment, &initJob, r.Scheme)

		err := r.Create(ctx, &initJob)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s init job.", req.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", "InitJobCreationFailed", fmt.Sprintf("error creating %s init job: %v", req.Name, err), metav1.ConditionFalse)
			return ctrl.Result{}, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)}), true
		}
		// Update the status of the OdooDeployment
		logger.Info(fmt.Sprintf("InitJob %s created", initJob.Name))
		r.OdooDeployment.Status.CurrentInitJob = odoov1.DBInitjob{
			Name:      initJob.Name,
			Namespace: r.OdooDeployment.Namespace,
			Modules:   modulesToInstall,
		}
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorSucceeded", "InitJobCreated", fmt.Sprintf("InitJob %s created", initJob.Name), metav1.ConditionTrue)

		return ctrl.Result{RequeueAfter: 30 * time.Second}, utilerrors.NewAggregate([]error{nil, r.Status().Update(ctx, r.OdooDeployment)}), true
	}

	return ctrl.Result{}, nil, false
}
