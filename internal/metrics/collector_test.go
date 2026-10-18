package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

func upgrading() *odoov1.OdooDeployment {
	replicas := int32(2)
	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	changed := metav1.NewTime(time.Unix(1_700_000_100, 0))
	return &odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "ababiel-pr-1", Namespace: "ababiel-preview"},
		Spec:       odoov1.OdooDeploymentSpec{Image: "odoo:b", Replicas: &replicas},
		Status: odoov1.OdooDeploymentStatus{
			Phase:                   odoov1.PhaseUpgrading,
			AppliedImage:            "odoo:a",
			Database:                odoov1.OdooDatabaseStatus{Name: "pr_1", ProvisionedBy: odoov1.DatabaseProvisionedByOperator},
			CurrentInitJob:          odoov1.MaintenanceJobStatus{Name: "ababiel-pr-1-upgrade-x", Kind: odoov1.JobKindUpgrade, StartedAt: &started},
			LastMaintenanceDuration: &metav1.Duration{Duration: 95 * time.Second},
			Conditions: []metav1.Condition{
				{Type: odoov1.ConditionReady, Status: metav1.ConditionFalse, Reason: odoov1.ReasonUpgradeJobRunning, LastTransitionTime: changed},
				{Type: odoov1.ConditionDatabaseReady, Status: metav1.ConditionTrue, Reason: odoov1.ReasonDatabaseCreated, LastTransitionTime: changed},
			},
		},
	}
}

func newCollector(t *testing.T, elected <-chan struct{}, objs ...*odoov1.OdooDeployment) *Collector {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := odoov1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &Collector{Reader: b.Build(), Elected: elected}
}

func TestCollectorReportsStatus(t *testing.T) {
	elected := make(chan struct{})
	close(elected)
	c := newCollector(t, elected, upgrading())

	want := `
# HELP odoo_operator_deployment_condition Always 1, one series per status condition; status is true, false or unknown.
# TYPE odoo_operator_deployment_condition gauge
odoo_operator_deployment_condition{name="ababiel-pr-1",namespace="ababiel-preview",reason="DatabaseCreated",status="true",type="DatabaseReady"} 1
odoo_operator_deployment_condition{name="ababiel-pr-1",namespace="ababiel-preview",reason="UpgradeJobRunning",status="false",type="Ready"} 1
# HELP odoo_operator_deployment_condition_last_transition_timestamp_seconds When each status condition last changed status, as a Unix timestamp.
# TYPE odoo_operator_deployment_condition_last_transition_timestamp_seconds gauge
odoo_operator_deployment_condition_last_transition_timestamp_seconds{name="ababiel-pr-1",namespace="ababiel-preview",type="DatabaseReady"} 1.7000001e+09
odoo_operator_deployment_condition_last_transition_timestamp_seconds{name="ababiel-pr-1",namespace="ababiel-preview",type="Ready"} 1.7000001e+09
# HELP odoo_operator_deployment_database_ready 1 while the DatabaseReady condition is true; provisioned_by is operator or external.
# TYPE odoo_operator_deployment_database_ready gauge
odoo_operator_deployment_database_ready{name="ababiel-pr-1",namespace="ababiel-preview",provisioned_by="operator"} 1
# HELP odoo_operator_deployment_image_pending 1 while spec.image differs from status.appliedImage (an image check or upgrade is pending).
# TYPE odoo_operator_deployment_image_pending gauge
odoo_operator_deployment_image_pending{name="ababiel-pr-1",namespace="ababiel-preview"} 1
# HELP odoo_operator_deployment_info Always 1; the labels are the OdooDeployment's spec.image, status.appliedImage and database name.
# TYPE odoo_operator_deployment_info gauge
odoo_operator_deployment_info{applied_image="odoo:a",database="pr_1",image="odoo:b",name="ababiel-pr-1",namespace="ababiel-preview"} 1
# HELP odoo_operator_deployment_job_start_timestamp_seconds When the running init or upgrade Job was created, as a Unix timestamp. Absent while no Job runs.
# TYPE odoo_operator_deployment_job_start_timestamp_seconds gauge
odoo_operator_deployment_job_start_timestamp_seconds{kind="upgrade",name="ababiel-pr-1",namespace="ababiel-preview"} 1.7e+09
# HELP odoo_operator_deployment_last_upgrade_duration_seconds status.lastMaintenanceDuration: how long the last successful upgrade Job took.
# TYPE odoo_operator_deployment_last_upgrade_duration_seconds gauge
odoo_operator_deployment_last_upgrade_duration_seconds{name="ababiel-pr-1",namespace="ababiel-preview"} 95
# HELP odoo_operator_deployment_phase 1 for the OdooDeployment's current status.phase, 0 for the others.
# TYPE odoo_operator_deployment_phase gauge
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="BackingUp"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Failed"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Initializing"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Pending"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Restoring"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Running"} 0
odoo_operator_deployment_phase{name="ababiel-pr-1",namespace="ababiel-preview",phase="Upgrading"} 1
# HELP odoo_operator_deployment_replicas_desired spec.replicas: Odoo pods the OdooDeployment runs outside maintenance.
# TYPE odoo_operator_deployment_replicas_desired gauge
odoo_operator_deployment_replicas_desired{name="ababiel-pr-1",namespace="ababiel-preview"} 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorReportsNothingUntilElected(t *testing.T) {
	c := newCollector(t, make(chan struct{}), upgrading())
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Fatalf("a replica that does not lead reported %d series", n)
	}
}

func TestCollectorDropsDeletedDeployments(t *testing.T) {
	c := newCollector(t, nil)
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Fatalf("no OdooDeployments, %d series", n)
	}
}

func TestForgetDropsCounters(t *testing.T) {
	Track("ns", "gone")
	JobFinished("ns", "gone", "", JobFailed, time.Minute)
	if v := testutil.ToFloat64(jobsTotal.WithLabelValues("ns", "gone", odoov1.JobKindInit, JobFailed)); v != 1 {
		t.Fatalf("an empty kind counts as init, got %v", v)
	}
	Forget("ns", "gone")
	if n := testutil.CollectAndCount(jobsTotal, "odoo_operator_jobs_total"); n != 0 {
		t.Fatalf("%d series left after Forget", n)
	}
}
