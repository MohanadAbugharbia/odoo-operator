package reconcileloops

import (
	"context"
	"fmt"

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
	"github.com/google/go-cmp/cmp"
)

type OdooPollServiceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

func (r *OdooPollServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (corev1.Service, error) {
	logger := log.FromContext(ctx)

	// Check if the http service already exists, if not create a new one
	pollService := corev1.Service{}
	createPollService := false
	pollServiceNamespacedName := types.NamespacedName{
		Name:      r.OdooDeployment.GetPollServiceName(),
		Namespace: r.OdooDeployment.Namespace,
	}
	err := r.Get(ctx, pollServiceNamespacedName, &pollService)
	if err != nil && errors.IsNotFound(err) {
		createPollService = true
	} else if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s service.", pollServiceNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedGetPollService, fmt.Sprintf("error creating %s service: %v", pollServiceNamespacedName.Name, err), metav1.ConditionFalse)
		return pollService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	pollServiceTemplate := r.OdooDeployment.GetPollServiceTemplate()

	pollServiceTemplate.Spec.ClusterIP = pollService.Spec.ClusterIP
	pollServiceTemplate.Spec.ClusterIPs = pollService.Spec.ClusterIPs
	pollServiceTemplate.Spec.IPFamilies = pollService.Spec.IPFamilies
	pollServiceTemplate.Spec.IPFamilyPolicy = pollService.Spec.IPFamilyPolicy

	ctrl.SetControllerReference(r.OdooDeployment, &pollService, r.Scheme)
	if createPollService {
		logger.Info(fmt.Sprintf("Creating a new service for %s", pollServiceNamespacedName.Name))
		pollService.Spec = pollServiceTemplate.Spec
		pollService.Name = pollServiceNamespacedName.Name
		pollService.Namespace = pollServiceNamespacedName.Namespace
		err = r.Create(ctx, &pollService)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s service.", pollServiceNamespacedName.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedCreatePollService, fmt.Sprintf("error creating %s service: %v", pollServiceNamespacedName.Name, err), metav1.ConditionFalse)
			return pollService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	} else if diff := cmp.Diff(pollService.Spec, pollServiceTemplate.Spec); diff != "" {
		logger.V(1).Info(fmt.Sprintf("Diff: %s", diff))
		logger.Info(fmt.Sprintf("Updating service %s spec", pollServiceNamespacedName.Name))
		pollService.Spec = pollServiceTemplate.Spec
		err = r.Update(ctx, &pollService)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error updating %s service.", pollServiceNamespacedName.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedUpdatePollService, fmt.Sprintf("error updating %s service: %v", pollServiceNamespacedName.Name, err), metav1.ConditionFalse)
			return pollService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}

	return pollService, nil
}
