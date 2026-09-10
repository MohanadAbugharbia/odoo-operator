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
	"github.com/MohanadAbugharbia/odoo-operator/pkg/utils"
)

// EnsureAdminPasswordSecret creates the Odoo admin password Secret when it is
// missing and fills in a random password when the "password" key is absent.
// An existing password is never rotated. Secrets named by the user
// (spec.config.adminPasswordSecretName) are not owned by the OdooDeployment.
func EnsureAdminPasswordSecret(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) (*corev1.Secret, error) {
	logger := log.FromContext(ctx)
	name := od.CreateOdooAdminPasswordSecretNamespacedName()
	secret := &corev1.Secret{}
	secret.Name = name.Name
	secret.Namespace = name.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if len(secret.Data["password"]) == 0 {
			password, err := utils.GenerateSecurePassword()
			if err != nil {
				return err
			}
			if secret.Data == nil {
				secret.Data = map[string][]byte{}
			}
			secret.Data["password"] = []byte(password)
		}
		if od.AdminPasswordSecretIsUserNamed() {
			return nil
		}
		return controllerutil.SetControllerReference(od, secret, scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("admin password secret %s: %w", name.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logger.Info("Reconciled admin password secret", "secret", name.Name, "result", result)
	}
	return secret, nil
}
