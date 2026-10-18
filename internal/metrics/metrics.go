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

// Package metrics holds the operator's own Prometheus metrics. They are
// registered on controller-runtime's registry, so they are served from the
// manager's /metrics endpoint next to the controller-runtime defaults.
//
// Two kinds live here: event counters that the reconciler increments when
// something happens (a Job finished, an image could not be pulled), and the
// Collector in collector.go, which reads every OdooDeployment's status at
// scrape time.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// Labels shared by the per-OdooDeployment series.
const (
	labelNamespace = "namespace"
	labelStore     = "store"
	labelName      = "name"
	labelKind      = "kind"
	labelResult    = "result"
)

// Results of a maintenance Job.
const (
	JobSucceeded  = "succeeded"
	JobFailed     = "failed"
	JobSuperseded = "superseded"
)

// Database operations and their results.
const (
	DatabaseCreate = "create"
	DatabaseAdopt  = "adopt"
	DatabaseDrop   = "drop"

	DatabaseSucceeded = "succeeded"
	DatabaseFailed    = "failed"
	DatabaseRefused   = "refused"
)

// Backup and restore results and restore modes.
const (
	BackupSucceeded = "succeeded"
	BackupFailed    = "failed"

	RestoreModeRestore = "restore"
	RestoreModeUndo    = "undo"
	RestoreSucceeded   = "succeeded"
	RestoreFailed      = "failed"
)

var (
	jobKinds   = []string{odoov1.JobKindInit, odoov1.JobKindUpgrade}
	jobResults = []string{JobSucceeded, JobFailed, JobSuperseded}

	jobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_jobs_total",
		Help: "Init and upgrade Jobs that finished, by result (succeeded, failed, superseded). Each Job is counted once.",
	}, []string{labelNamespace, labelName, labelKind, labelResult})

	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "odoo_operator_job_duration_seconds",
		Help:    "How long init and upgrade Jobs that succeeded or failed took, from creation to completion or failure.",
		Buckets: []float64{30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600},
	}, []string{labelKind, labelResult})

	imagePullFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_image_pull_failures_total",
		Help: "Times a new image could not be pulled, counted once each time an OdooDeployment becomes Degraded with ImagePullFailed.",
	}, []string{labelNamespace, labelName})

	databaseOperations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_database_operations_total",
		Help: "Database create, adopt and drop operations, by result (succeeded, failed, refused). A failure is counted once until it clears.",
	}, []string{"operation", labelResult})

	pagePublishErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_maintenance_page_publish_errors_total",
		Help: "Failed attempts to publish an OdooDeployment's status to its maintenance page.",
	}, []string{labelNamespace, labelName})

	backupsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_backups_total",
		Help: "Backups (OdooBackup) that finished, by kind (manual, scheduled, pre-upgrade) and result (succeeded, failed).",
	}, []string{labelKind, labelResult})

	backupDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "odoo_operator_backup_duration_seconds",
		Help:    "How long backups that succeeded took, from the start of their first Job to completion.",
		Buckets: []float64{30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 21600},
	}, []string{labelKind})

	restoresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "odoo_operator_restores_total",
		Help: "In-place restores (OdooRestore) that finished, by mode (restore, undo) and result (succeeded, failed).",
	}, []string{"mode", labelResult})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "odoo_operator_build_info",
		Help: "Always 1; the version label is the running operator release.",
	}, []string{"version"})
)

// Metric types, as the metrics reference names them.
const (
	typeCounter   = "counter"
	typeGauge     = "gauge"
	typeHistogram = "histogram"
)

// registered are the metrics init registers, with their types for the
// metrics reference (Reference).
var registered = []struct {
	collector prometheus.Collector
	typ       string
}{
	{jobsTotal, typeCounter},
	{jobDuration, typeHistogram},
	{imagePullFailures, typeCounter},
	{databaseOperations, typeCounter},
	{pagePublishErrors, typeCounter},
	{buildInfo, typeGauge},
	{backupsTotal, typeCounter},
	{backupDuration, typeHistogram},
	{restoresTotal, typeCounter},
}

func init() {
	for _, m := range registered {
		ctrlmetrics.Registry.MustRegister(m.collector)
	}
	for _, kind := range []string{odoov1.BackupKindManual, odoov1.BackupKindScheduled, odoov1.BackupKindPreUpgrade} {
		for _, res := range []string{BackupSucceeded, BackupFailed} {
			backupsTotal.WithLabelValues(kind, res)
		}
	}
	for _, mode := range []string{RestoreModeRestore, RestoreModeUndo} {
		for _, res := range []string{RestoreSucceeded, RestoreFailed} {
			restoresTotal.WithLabelValues(mode, res)
		}
	}
	for _, op := range []string{DatabaseCreate, DatabaseAdopt, DatabaseDrop} {
		for _, res := range []string{DatabaseSucceeded, DatabaseFailed, DatabaseRefused} {
			if res == DatabaseRefused && op != DatabaseDrop {
				continue
			}
			databaseOperations.WithLabelValues(op, res)
		}
	}
}

// SetBuildInfo publishes the running operator version.
func SetBuildInfo(version string) {
	buildInfo.Reset()
	buildInfo.WithLabelValues(version).Set(1)
}

// Track creates an OdooDeployment's counters at zero, so the first real
// event shows up in increase() instead of being the series' first sample.
func Track(namespace, name string) {
	for _, kind := range jobKinds {
		for _, result := range jobResults {
			jobsTotal.WithLabelValues(namespace, name, kind, result)
		}
	}
	imagePullFailures.WithLabelValues(namespace, name)
	pagePublishErrors.WithLabelValues(namespace, name)
}

// Forget drops an OdooDeployment's counters once it is gone, so a closed
// preview does not stay on /metrics until the operator restarts.
func Forget(namespace, name string) {
	labels := prometheus.Labels{labelNamespace: namespace, labelName: name}
	jobsTotal.DeletePartialMatch(labels)
	imagePullFailures.DeletePartialMatch(labels)
	pagePublishErrors.DeletePartialMatch(labels)
}

// JobFinished counts a Job that succeeded or failed and observes how long it took.
func JobFinished(namespace, name, kind, result string, took time.Duration) {
	kind = normalizeKind(kind)
	jobsTotal.WithLabelValues(namespace, name, kind, result).Inc()
	jobDuration.WithLabelValues(kind, result).Observe(took.Seconds())
}

// JobWasSuperseded counts a Job replaced before it ran (its image could not be
// pulled and spec.image moved on). It has no meaningful duration.
func JobWasSuperseded(namespace, name, kind string) {
	jobsTotal.WithLabelValues(namespace, name, normalizeKind(kind), JobSuperseded).Inc()
}

// normalizeKind maps a status written before Kind existed to init, as the
// controller does.
func normalizeKind(kind string) string {
	if kind != odoov1.JobKindUpgrade {
		return odoov1.JobKindInit
	}
	return kind
}

// ImagePullFailed counts an OdooDeployment becoming Degraded with ImagePullFailed.
func ImagePullFailed(namespace, name string) {
	imagePullFailures.WithLabelValues(namespace, name).Inc()
}

// DatabaseOperation counts a database operation.
func DatabaseOperation(operation, result string) {
	databaseOperations.WithLabelValues(operation, result).Inc()
}

// MaintenancePagePublishFailed counts a failed maintenance page update.
func MaintenancePagePublishFailed(namespace, name string) {
	pagePublishErrors.WithLabelValues(namespace, name).Inc()
}

// BackupFinished counts a backup that ended and observes how long a
// successful one took.
func BackupFinished(kind, result string, took time.Duration) {
	backupsTotal.WithLabelValues(kind, result).Inc()
	if result == BackupSucceeded && took > 0 {
		backupDuration.WithLabelValues(kind).Observe(took.Seconds())
	}
}

// RestoreFinished counts a restore that ended.
func RestoreFinished(mode, result string) {
	restoresTotal.WithLabelValues(mode, result).Inc()
}
