package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
# HELP odoo_operator_backup_enabled 1 when the OdooDeployment takes scheduled backups (spec.backup.schedule is set).
# TYPE odoo_operator_backup_enabled gauge
odoo_operator_backup_enabled{name="ababiel-pr-1",namespace="ababiel-preview"} 0
# HELP odoo_operator_restore_drill_enabled 1 when the OdooDeployment runs restore drills (spec.backup.drill.schedule is set).
# TYPE odoo_operator_restore_drill_enabled gauge
odoo_operator_restore_drill_enabled{name="ababiel-pr-1",namespace="ababiel-preview"} 0
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

func TestCollectorReportsBackupsDrillsAndStores(t *testing.T) {
	at := func(sec int64) *metav1.Time { t := metav1.NewTime(time.Unix(sec, 0)); return &t }
	od := &odoov1.OdooDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "odoo"},
		Spec: odoov1.OdooDeploymentSpec{Backup: odoov1.OdooBackupConfig{
			Schedule: "0 3 * * *", Drill: odoov1.OdooBackupDrill{Schedule: "0 5 * * 0"},
		}},
		Status: odoov1.OdooDeploymentStatus{Backup: odoov1.OdooBackupScheduleStatus{
			LastDrill:               &odoov1.OdooDrillResult{Succeeded: false, CompletedAt: at(1_700_000_900)},
			LastSuccessfulDrillTime: at(1_700_000_500),
		}},
	}
	older := &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-1", Namespace: "odoo"},
		Spec:       odoov1.OdooBackupSpec{OdooDeployment: "shop"},
		Status: odoov1.OdooBackupStatus{Phase: odoov1.BackupPhaseCompleted, CompletedAt: at(1_700_000_000),
			Copies: []odoov1.BackupCopy{{Store: "offsite", Error: "old failure"}}},
	}
	newer := &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-2", Namespace: "odoo"},
		Spec:       odoov1.OdooBackupSpec{OdooDeployment: "shop"},
		Status: odoov1.OdooBackupStatus{Phase: odoov1.BackupPhasePartiallyCompleted, CompletedAt: at(1_700_000_100),
			Copies: []odoov1.BackupCopy{{Store: "offsite", Error: "unreachable"}, {Store: "cold", SnapshotID: "ab"}}},
	}
	failedRestore := &odoov1.OdooRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-r", Namespace: "odoo"},
		Spec:       odoov1.OdooRestoreSpec{OdooDeployment: "shop", Backup: "shop-1"},
		Status:     odoov1.OdooRestoreStatus{Phase: odoov1.RestorePhaseFailed, CompletedAt: at(1_700_000_200)},
	}
	store := &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: "odoo"},
		Status: odoov1.OdooBackupStoreStatus{Ready: true, StoredBytes: 4096,
			LastCheck: &odoov1.StoreTaskStatus{Time: at(1_700_000_300), Result: odoov1.StoreTaskFailed},
			LastPrune: &odoov1.StoreTaskStatus{Time: at(1_700_000_400), Result: odoov1.StoreTaskPassed}},
	}
	c := newCollectorWith(t, od, older, newer, failedRestore, store)
	want := `
# HELP odoo_operator_backup_enabled 1 when the OdooDeployment takes scheduled backups (spec.backup.schedule is set).
# TYPE odoo_operator_backup_enabled gauge
odoo_operator_backup_enabled{name="shop",namespace="odoo"} 1
# HELP odoo_operator_backup_last_copy_failed Per copy store, 1 when the instance's most recently finished backup could not be copied there.
# TYPE odoo_operator_backup_last_copy_failed gauge
odoo_operator_backup_last_copy_failed{name="shop",namespace="odoo",store="cold"} 0
odoo_operator_backup_last_copy_failed{name="shop",namespace="odoo",store="offsite"} 1
# HELP odoo_operator_backup_store_stored_bytes What the instance repositories in the store hold together, as of its latest check or prune.
# TYPE odoo_operator_backup_store_stored_bytes gauge
odoo_operator_backup_store_stored_bytes{namespace="odoo",store="main"} 4096
# HELP odoo_operator_backup_store_task_last_failed 1 when the store's latest check or prune (task) failed.
# TYPE odoo_operator_backup_store_task_last_failed gauge
odoo_operator_backup_store_task_last_failed{namespace="odoo",store="main",task="check"} 1
odoo_operator_backup_store_task_last_failed{namespace="odoo",store="main",task="prune"} 0
# HELP odoo_operator_backup_store_task_last_timestamp_seconds When the store's latest check or prune (task) finished, as a Unix timestamp.
# TYPE odoo_operator_backup_store_task_last_timestamp_seconds gauge
odoo_operator_backup_store_task_last_timestamp_seconds{namespace="odoo",store="main",task="check"} 1.7000003e+09
odoo_operator_backup_store_task_last_timestamp_seconds{namespace="odoo",store="main",task="prune"} 1.7000004e+09
# HELP odoo_operator_restore_drill_enabled 1 when the OdooDeployment runs restore drills (spec.backup.drill.schedule is set).
# TYPE odoo_operator_restore_drill_enabled gauge
odoo_operator_restore_drill_enabled{name="shop",namespace="odoo"} 1
# HELP odoo_operator_restore_drill_last_failed 1 when the instance's latest restore drill failed.
# TYPE odoo_operator_restore_drill_last_failed gauge
odoo_operator_restore_drill_last_failed{name="shop",namespace="odoo"} 1
# HELP odoo_operator_restore_drill_last_success_timestamp_seconds When a restore drill of the instance last passed, as a Unix timestamp. Absent until one passes.
# TYPE odoo_operator_restore_drill_last_success_timestamp_seconds gauge
odoo_operator_restore_drill_last_success_timestamp_seconds{name="shop",namespace="odoo"} 1.7000005e+09
# HELP odoo_operator_restore_last_failed 1 when the instance's most recently finished OdooRestore failed.
# TYPE odoo_operator_restore_last_failed gauge
odoo_operator_restore_last_failed{name="shop",namespace="odoo"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"odoo_operator_backup_enabled", "odoo_operator_backup_last_copy_failed", "odoo_operator_backup_store_stored_bytes",
		"odoo_operator_backup_store_task_last_failed", "odoo_operator_backup_store_task_last_timestamp_seconds",
		"odoo_operator_restore_drill_enabled", "odoo_operator_restore_drill_last_failed",
		"odoo_operator_restore_drill_last_success_timestamp_seconds", "odoo_operator_restore_last_failed"); err != nil {
		t.Fatal(err)
	}
}

func newCollectorWith(t *testing.T, objs ...client.Object) *Collector {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := odoov1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &Collector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource().Build()}
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
