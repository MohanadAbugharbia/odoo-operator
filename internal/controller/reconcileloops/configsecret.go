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

// EnsureConfigSecret writes the rendered odoo.conf into the config Secret and
// returns the Secret together with the config hash that rolls the pods.
func EnsureConfigSecret(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
	serializedConfig string,
) (*corev1.Secret, string, error) {
	logger := log.FromContext(ctx)
	name := od.CreateOdooConfigSecretNamespacedName()
	secret := &corev1.Secret{}
	secret.Name = name.Name
	secret.Namespace = name.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		secret.Data = map[string][]byte{"odoo.conf": []byte(serializedConfig)}
		return controllerutil.SetControllerReference(od, secret, scheme)
	})
	if err != nil {
		return nil, "", fmt.Errorf("config secret %s: %w", name.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logger.Info("Reconciled config secret", "secret", name.Name, "result", result)
	}
	return secret, odoov1.ConfigHash(serializedConfig), nil
}
