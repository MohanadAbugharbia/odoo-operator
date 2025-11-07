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

	pollService = r.OdooDeployment.GetPollServiceTemplate()
	pollService.Name = pollServiceNamespacedName.Name
	pollService.Namespace = pollServiceNamespacedName.Namespace

	ctrl.SetControllerReference(r.OdooDeployment, &pollService, r.Scheme)
	if createPollService {
		logger.Info(fmt.Sprintf("Creating a new service for %s", pollServiceNamespacedName.Name))
		err = r.Create(ctx, &pollService)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s service.", pollServiceNamespacedName.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedCreatePollService, fmt.Sprintf("error creating %s service: %v", pollServiceNamespacedName.Name, err), metav1.ConditionFalse)
			return pollService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}
	// No need to update if service already exists and hasn't changed

	if false {
		logger.Error(err, fmt.Sprintf("error creating or updating %s service.", pollServiceNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedCreatePollService, fmt.Sprintf("error creating or updating %s service: %v", pollServiceNamespacedName.Name, err), metav1.ConditionFalse)
		return pollService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	return pollService, nil
}
