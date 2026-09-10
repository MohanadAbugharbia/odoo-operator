package reconcileloops

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// EnsurePollService reconciles the <name>-poll Service (port 8072).
func EnsurePollService(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) (*corev1.Service, error) {
	return ensureService(ctx, c, scheme, od, od.GetPollServiceTemplate())
}
