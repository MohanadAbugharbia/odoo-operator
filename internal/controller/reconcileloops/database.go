package reconcileloops

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/database"
)

// ConnInfoFor maps the resolved connection details onto the provisioner's ConnInfo.
func ConnInfoFor(od *odoov1.OdooDeployment, conn odoov1.DatabaseConnectionDetails) database.ConnInfo {
	return database.ConnInfo{
		Host:          conn.Host,
		Port:          conn.Port,
		User:          conn.User,
		Password:      conn.Password,
		MaintenanceDB: od.Spec.Database.MaintenanceDatabaseValue(),
		SSL:           conn.SSL,
	}
}

// DatabaseResult is the outcome of ReconcileDatabase.
type DatabaseResult struct {
	// Ready is true when the database exists and status.database is settled.
	Ready bool
	// Reason/Message feed the DatabaseReady (and, on failure, Degraded) condition.
	Reason  string
	Message string
	// Event, when set, is the reason of a Normal event to record (a transition happened).
	Event string
	// Requeue is the delay to retry with when not Ready.
	Requeue time.Duration
	// Fatal marks a state that no retry fixes (the resolved name changed).
	Fatal bool
}

// ReconcileDatabase settles status.database: it adopts a pre-existing
// database as "external", creates a missing one as "operator" (recording the
// Provisioning state through persist before CREATE DATABASE so a crash can
// never leave an unrecorded database behind), or reports it missing when
// createPolicy is Never.
func ReconcileDatabase(
	ctx context.Context,
	prov database.Provisioner,
	od *odoov1.OdooDeployment,
	conn odoov1.DatabaseConnectionDetails,
	persist func(context.Context) error,
) (DatabaseResult, error) {
	logger := log.FromContext(ctx)
	info := ConnInfoFor(od, conn)
	name := conn.Name
	tag := database.NewOwnerTag(od.Namespace, od.Name)
	st := &od.Status.Database

	if st.Name != "" && st.Name != name {
		return DatabaseResult{
			Fatal:  true,
			Reason: odoov1.ReasonDatabaseNameChanged,
			Message: fmt.Sprintf("status.database.name is %q but spec.database now resolves to %q; "+
				"the recorded database is never renamed or dropped implicitly, create a new OdooDeployment instead",
				st.Name, name),
		}, nil
	}

	if st.Name != "" && st.State == odoov1.DatabaseStateReady {
		reason := odoov1.ReasonDatabaseCreated
		if st.ProvisionedBy == odoov1.DatabaseProvisionedByExternal {
			reason = odoov1.ReasonDatabaseAdopted
		}
		return DatabaseResult{Ready: true, Reason: reason, Message: fmt.Sprintf("database %q on %s (provisionedBy: %s)", st.Name, st.Host, st.ProvisionedBy)}, nil
	}

	if st.Name != "" && st.State == odoov1.DatabaseStateProvisioning {
		// A previous reconcile recorded the intent to create and may have
		// crashed anywhere between here and the status update.
		exists, err := prov.Exists(ctx, info, name)
		if err != nil {
			return DatabaseResult{Reason: odoov1.ReasonDatabaseConnectionFailed, Requeue: 30 * time.Second}, err
		}
		if exists {
			logger.Info("Database exists after an interrupted create, re-applying owner tag", "database", name)
			if err := prov.Tag(ctx, info, name, tag); err != nil {
				return DatabaseResult{Reason: odoov1.ReasonDatabaseCreateFailed, Requeue: 30 * time.Second}, err
			}
		} else {
			if err := prov.Create(ctx, info, name, tag); err != nil {
				return DatabaseResult{Reason: odoov1.ReasonDatabaseCreateFailed, Requeue: 30 * time.Second}, err
			}
		}
		st.State = odoov1.DatabaseStateReady
		if st.CreatedAt == nil {
			now := metav1.Now()
			st.CreatedAt = &now
		}
		return DatabaseResult{
			Ready:   true,
			Reason:  odoov1.ReasonDatabaseCreated,
			Event:   odoov1.ReasonDatabaseCreated,
			Message: fmt.Sprintf("created database %q on %s", name, conn.Host),
		}, nil
	}

	// Nothing recorded yet.
	exists, err := prov.Exists(ctx, info, name)
	if err != nil {
		return DatabaseResult{Reason: odoov1.ReasonDatabaseConnectionFailed, Requeue: 30 * time.Second}, err
	}
	if exists {
		*st = odoov1.OdooDatabaseStatus{
			Name:          name,
			Host:          conn.Host,
			ProvisionedBy: odoov1.DatabaseProvisionedByExternal,
			State:         odoov1.DatabaseStateReady,
		}
		return DatabaseResult{
			Ready:   true,
			Reason:  odoov1.ReasonDatabaseAdopted,
			Event:   odoov1.ReasonDatabaseAdopted,
			Message: fmt.Sprintf("adopted pre-existing database %q on %s; the operator will never drop it", name, conn.Host),
		}, nil
	}
	if od.Spec.Database.CreatePolicyValue() == odoov1.DatabaseCreatePolicyNever {
		return DatabaseResult{
			Reason:  odoov1.ReasonDatabaseMissing,
			Message: fmt.Sprintf("database %q does not exist on %s and spec.database.createPolicy is Never", name, conn.Host),
			Requeue: time.Minute,
		}, nil
	}

	*st = odoov1.OdooDatabaseStatus{
		Name:          name,
		Host:          conn.Host,
		ProvisionedBy: odoov1.DatabaseProvisionedByOperator,
		State:         odoov1.DatabaseStateProvisioning,
	}
	if err := persist(ctx); err != nil {
		return DatabaseResult{Reason: odoov1.ReasonDatabaseProvisioning, Requeue: 10 * time.Second}, fmt.Errorf("record database provisioning: %w", err)
	}
	logger.Info("Creating database", "database", name, "host", conn.Host)
	if err := prov.Create(ctx, info, name, tag); err != nil {
		return DatabaseResult{Reason: odoov1.ReasonDatabaseCreateFailed, Requeue: 30 * time.Second}, err
	}
	now := metav1.Now()
	st.State = odoov1.DatabaseStateReady
	st.CreatedAt = &now
	return DatabaseResult{
		Ready:   true,
		Reason:  odoov1.ReasonDatabaseCreated,
		Event:   odoov1.ReasonDatabaseCreated,
		Message: fmt.Sprintf("created database %q on %s", name, conn.Host),
	}, nil
}
