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

type OdooFilestoreReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

func (r *OdooFilestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (corev1.PersistentVolumeClaim, error) {
	logger := log.FromContext(ctx)

	// Check if the PVC already exists, if not create a new one
	pvc := corev1.PersistentVolumeClaim{}
	createPvc := false
	err := r.Get(ctx, req.NamespacedName, &pvc)
	if err != nil && errors.IsNotFound(err) {
		// Create a new PVC for the OdooDeployment if it does not exist
		createPvc = true
	} else if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s pvc.", req.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonPvcNotAvailable, fmt.Sprintf("error creating %s pvc: %v", req.Name, err), metav1.ConditionFalse)
		return pvc, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	// Update the status of the OdooDeployment

	pvc = r.OdooDeployment.GetPvcTemplate()
	ctrl.SetControllerReference(r.OdooDeployment, &pvc, r.Scheme)

	// Here we do not try to update the PVC, because the spec field is immutable
	if createPvc {
		logger.Info(fmt.Sprintf("Creating a new PVC for %s", req.Name))
		err = r.Create(ctx, &pvc)
	}
	if err != nil {
		logger.Error(err, fmt.Sprintf("error creating or updating %s pvc.", pvc.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonPvcCreationFailed, fmt.Sprintf("error creating or updating %s pvc: %v", req.Name, err), metav1.ConditionFalse)
		return pvc, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}
	return pvc, nil
}
