/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// DefaultListTimeout bounds the List a scrape makes, so a scrape before the
// cache has synced returns nothing instead of hanging.
const DefaultListTimeout = 5 * time.Second

var phases = []string{
	odoov1.PhasePending,
	odoov1.PhaseInitializing,
	odoov1.PhaseUpgrading,
	odoov1.PhaseRunning,
	odoov1.PhaseFailed,
	odoov1.PhaseBackingUp,
	odoov1.PhaseRestoring,
}

var (
	descInfo = prometheus.NewDesc("odoo_operator_deployment_info",
		"Always 1; the labels are the OdooDeployment's spec.image, status.appliedImage and database name.",
		[]string{labelNamespace, labelName, "image", "applied_image", "database"}, nil)
	descPhase = prometheus.NewDesc("odoo_operator_deployment_phase",
		"1 for the OdooDeployment's current status.phase, 0 for the others.",
		[]string{labelNamespace, labelName, "phase"}, nil)
	descCondition = prometheus.NewDesc("odoo_operator_deployment_condition",
		"Always 1, one series per status condition; status is true, false or unknown.",
		[]string{labelNamespace, labelName, "type", "status", "reason"}, nil)
	descConditionTransition = prometheus.NewDesc("odoo_operator_deployment_condition_last_transition_timestamp_seconds",
		"When each status condition last changed status, as a Unix timestamp.",
		[]string{labelNamespace, labelName, "type"}, nil)
	descReplicasDesired = prometheus.NewDesc("odoo_operator_deployment_replicas_desired",
		"spec.replicas: Odoo pods the OdooDeployment runs outside maintenance.",
		[]string{labelNamespace, labelName}, nil)
	descImagePending = prometheus.NewDesc("odoo_operator_deployment_image_pending",
		"1 while spec.image differs from status.appliedImage (an image check or upgrade is pending).",
		[]string{labelNamespace, labelName}, nil)
	descJobStart = prometheus.NewDesc("odoo_operator_deployment_job_start_timestamp_seconds",
		"When the running init or upgrade Job was created, as a Unix timestamp. Absent while no Job runs.",
		[]string{labelNamespace, labelName, labelKind}, nil)
	descLastUpgrade = prometheus.NewDesc("odoo_operator_deployment_last_upgrade_duration_seconds",
		"status.lastMaintenanceDuration: how long the last successful upgrade Job took.",
		[]string{labelNamespace, labelName}, nil)
	descDatabaseReady = prometheus.NewDesc("odoo_operator_deployment_database_ready",
		"1 while the DatabaseReady condition is true; provisioned_by is operator or external.",
		[]string{labelNamespace, labelName, "provisioned_by"}, nil)
)

var (
	descBackupLastSuccess = prometheus.NewDesc("odoo_operator_backup_last_success_timestamp_seconds",
		"When the instance's newest successful backup (Completed or CompletedWithWarnings) completed, as a Unix timestamp.",
		[]string{labelNamespace, labelName}, nil)
	descBackupLastSize = prometheus.NewDesc("odoo_operator_backup_last_size_bytes",
		"Sizes of the instance's newest successful backup: part is database, filestore or added (new data uploaded).",
		[]string{labelNamespace, labelName, "part"}, nil)
	descBackupMissingFiles = prometheus.NewDesc("odoo_operator_backup_missing_files",
		"Attachments the database refers to that the instance's newest successful backup could not include.",
		[]string{labelNamespace, labelName}, nil)
	descBackupLastFailed = prometheus.NewDesc("odoo_operator_backup_last_failed",
		"1 when the instance's most recently finished backup failed.",
		[]string{labelNamespace, labelName}, nil)
	descStoreReady = prometheus.NewDesc("odoo_operator_backup_store_ready",
		"1 when the OdooBackupStore's last probe could write, read and delete a file.",
		[]string{labelNamespace, "store"}, nil)
)

// Collector reports every OdooDeployment's status at scrape time. Reading
// at scrape time means a deleted OdooDeployment simply stops appearing.
type Collector struct {
	// Reader lists OdooDeployments, normally the manager's cached client.
	Reader client.Reader
	// Elected is closed once this replica leads (the manager's Elected()).
	// Until then the collector reports nothing, so two operator pods during
	// a rollout never report the same OdooDeployment twice. Nil: always report.
	Elected <-chan struct{}
	// Timeout bounds the List; DefaultListTimeout when zero.
	Timeout time.Duration
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		descInfo, descPhase, descCondition, descConditionTransition, descReplicasDesired,
		descImagePending, descJobStart, descLastUpgrade, descDatabaseReady,
		descBackupLastSuccess, descBackupLastSize, descBackupMissingFiles, descBackupLastFailed, descStoreReady,
	} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if c.Elected != nil {
		select {
		case <-c.Elected:
		default:
			return
		}
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultListTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	list := &odoov1.OdooDeploymentList{}
	if err := c.Reader.List(ctx, list); err != nil {
		logf.Log.WithName("metrics").Error(err, "could not list OdooDeployments for /metrics")
		return
	}
	for i := range list.Items {
		collectOne(ch, &list.Items[i])
	}
	backups := &odoov1.OdooBackupList{}
	if err := c.Reader.List(ctx, backups); err != nil {
		logf.Log.WithName("metrics").Error(err, "could not list OdooBackups for /metrics")
	} else {
		collectBackups(ch, backups.Items)
	}
	stores := &odoov1.OdooBackupStoreList{}
	if err := c.Reader.List(ctx, stores); err != nil {
		logf.Log.WithName("metrics").Error(err, "could not list OdooBackupStores for /metrics")
	} else {
		for _, st := range stores.Items {
			ch <- prometheus.MustNewConstMetric(descStoreReady, prometheus.GaugeValue, boolValue(st.Status.Ready), st.Namespace, st.Name)
		}
	}
}

// collectBackups reports, per instance, its newest successful backup and
// whether its most recently finished backup failed.
func collectBackups(ch chan<- prometheus.Metric, backups []odoov1.OdooBackup) {
	type instance struct{ namespace, name string }
	lastOK := map[instance]*odoov1.OdooBackup{}
	lastDone := map[instance]*odoov1.OdooBackup{}
	for i := range backups {
		b := &backups[i]
		if b.Status.CompletedAt == nil {
			continue
		}
		key := instance{b.Namespace, b.Spec.OdooDeployment}
		if cur := lastDone[key]; cur == nil || b.Status.CompletedAt.After(cur.Status.CompletedAt.Time) {
			lastDone[key] = b
		}
		switch b.Status.Phase {
		case odoov1.BackupPhaseCompleted, odoov1.BackupPhaseCompletedWithWarnings, odoov1.BackupPhasePartiallyCompleted:
			if cur := lastOK[key]; cur == nil || b.Status.CompletedAt.After(cur.Status.CompletedAt.Time) {
				lastOK[key] = b
			}
		}
	}
	for key, b := range lastOK {
		gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{key.namespace, key.name}, labels...)...)
		}
		gauge(descBackupLastSuccess, unixSeconds(*b.Status.CompletedAt))
		gauge(descBackupLastSize, float64(b.Status.DatabaseBytes), "database")
		gauge(descBackupLastSize, float64(b.Status.FilestoreBytes), "filestore")
		gauge(descBackupLastSize, float64(b.Status.AddedBytes), "added")
		gauge(descBackupMissingFiles, float64(b.Status.MissingFiles))
	}
	for key, b := range lastDone {
		ch <- prometheus.MustNewConstMetric(descBackupLastFailed, prometheus.GaugeValue,
			boolValue(b.Status.Phase == odoov1.BackupPhaseFailed), key.namespace, key.name)
	}
}

func collectOne(ch chan<- prometheus.Metric, od *odoov1.OdooDeployment) {
	ns, name := od.Namespace, od.Name
	st := &od.Status
	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, append([]string{ns, name}, labels...)...)
	}

	gauge(descInfo, 1, od.Spec.Image, st.AppliedImage, st.Database.Name)
	for _, phase := range phases {
		gauge(descPhase, boolValue(st.Phase == phase), phase)
	}
	for _, cond := range st.Conditions {
		gauge(descCondition, 1, cond.Type, strings.ToLower(string(cond.Status)), cond.Reason)
		if !cond.LastTransitionTime.IsZero() {
			gauge(descConditionTransition, unixSeconds(cond.LastTransitionTime), cond.Type)
		}
	}
	gauge(descReplicasDesired, float64(od.Spec.ReplicasValue()))
	gauge(descImagePending, boolValue(od.Spec.Image != st.AppliedImage))
	if job := st.CurrentInitJob; job.Name != "" && job.StartedAt != nil {
		kind := job.Kind
		if kind != odoov1.JobKindUpgrade {
			kind = odoov1.JobKindInit
		}
		gauge(descJobStart, unixSeconds(*job.StartedAt), kind)
	}
	if st.LastMaintenanceDuration != nil {
		gauge(descLastUpgrade, st.LastMaintenanceDuration.Seconds())
	}
	gauge(descDatabaseReady, boolValue(meta.IsStatusConditionTrue(st.Conditions, odoov1.ConditionDatabaseReady)), st.Database.ProvisionedBy)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func unixSeconds(t metav1.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}
