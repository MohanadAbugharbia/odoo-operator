package reconcileloops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// ensureService creates or updates a ClusterIP Service, touching only the
// selector, ports and type so that the allocated ClusterIP and IP families
// are kept.
func ensureService(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
	desired corev1.Service,
) (*corev1.Service, error) {
	logger := log.FromContext(ctx)
	service := &corev1.Service{}
	service.Name = desired.Name
	service.Namespace = desired.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c, service, func() error {
		service.Spec.Selector = desired.Spec.Selector
		service.Spec.Ports = desired.Spec.Ports
		service.Spec.Type = desired.Spec.Type
		return controllerutil.SetControllerReference(od, service, scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", desired.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logger.Info("Reconciled service", "service", desired.Name, "result", result)
	}
	return service, nil
}

// EnsureHttpService reconciles the <name>-http Service (port 8069).
func EnsureHttpService(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) (*corev1.Service, error) {
	return ensureService(ctx, c, scheme, od, od.GetHttpServiceTemplate())
}
