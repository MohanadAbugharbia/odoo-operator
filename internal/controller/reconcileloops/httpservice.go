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

type OdooHttpServiceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	OdooDeployment *odoov1.OdooDeployment
}

func (r *OdooHttpServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (corev1.Service, error) {
	logger := log.FromContext(ctx)

	// Check if the http service already exists, if not create a new one
	httpService := corev1.Service{}
	createHttpService := false
	httpServiceNamespacedName := types.NamespacedName{
		Name:      r.OdooDeployment.GetHttpServiceName(),
		Namespace: r.OdooDeployment.Namespace,
	}
	err := r.Get(ctx, httpServiceNamespacedName, &httpService)
	if err != nil && errors.IsNotFound(err) {
		createHttpService = true
	} else if err != nil {
		logger.Error(err, fmt.Sprintf("error creating %s service.", httpServiceNamespacedName.Name))
		utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedGetHttpService, fmt.Sprintf("error creating %s service: %v", req.Name, err), metav1.ConditionFalse)
		return httpService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
	}

	httpServiceTemplate := r.OdooDeployment.GetHttpServiceTemplate()

	httpServiceTemplate.Spec.ClusterIP = httpService.Spec.ClusterIP
	httpServiceTemplate.Spec.ClusterIPs = httpService.Spec.ClusterIPs
	httpServiceTemplate.Spec.IPFamilies = httpService.Spec.IPFamilies
	httpServiceTemplate.Spec.IPFamilyPolicy = httpService.Spec.IPFamilyPolicy

	ctrl.SetControllerReference(r.OdooDeployment, &httpService, r.Scheme)
	if createHttpService {
		logger.Info(fmt.Sprintf("Creating a new service for %s", req.Name))
		httpService.Spec = httpServiceTemplate.Spec
		httpService.Name = httpServiceNamespacedName.Name
		httpService.Namespace = httpServiceNamespacedName.Namespace
		err = r.Create(ctx, &httpService)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error creating %s service.", httpService.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedCreateHttpService, fmt.Sprintf("error creating %s service: %v", httpServiceNamespacedName.Name, err), metav1.ConditionFalse)
			return httpService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	} else if diff := cmp.Diff(httpService.Spec, httpServiceTemplate.Spec); diff != "" {
		logger.V(1).Info(fmt.Sprintf("Diff: %s", diff))
		httpService.Spec = httpServiceTemplate.Spec
		err = r.Update(ctx, &httpService)
		if err != nil {
			logger.Error(err, fmt.Sprintf("error updating %s service.", httpService.Name))
			utils.UpdateStatus(&r.OdooDeployment.Status.Conditions, "OperatorDegraded", odoov1.ReasonFailedUpdateHttpService, fmt.Sprintf("error updating %s service: %v", httpServiceNamespacedName.Name, err), metav1.ConditionFalse)
			return httpService, utilerrors.NewAggregate([]error{err, r.Status().Update(ctx, r.OdooDeployment)})
		}
	}

	return httpService, nil
}
