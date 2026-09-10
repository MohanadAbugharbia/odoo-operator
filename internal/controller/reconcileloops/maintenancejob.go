package reconcileloops

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// JobState summarises a maintenance Job.
type JobState string

const (
	// JobStateMissing: status.currentInitJob points at a Job that no longer exists (a deleted failed Job means "retry").
	JobStateMissing JobState = "Missing"
	// JobStateActive: the Job has not finished yet (running, pending, or unable to create its pod).
	JobStateActive JobState = "Active"
	// JobStateSucceeded: the Job completed.
	JobStateSucceeded JobState = "Succeeded"
	// JobStateFailed: the Job hit its backoff limit or active deadline.
	JobStateFailed JobState = "Failed"
)

// JobObservation is what ObserveMaintenanceJob learned about status.currentInitJob.
type JobObservation struct {
	State JobState
	Job   *batchv1.Job
	// FailureMessage is the Failed condition message when State is Failed.
	FailureMessage string
	// QuotaMessage carries the "exceeded quota" event message when the Job
	// cannot create its pod because of a ResourceQuota.
	QuotaMessage string
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == t && job.Status.Conditions[i].Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// ObserveMaintenanceJob looks up the Job recorded in status.currentInitJob.
// reader is an uncached client used for the Event lookup (field selectors are
// not indexed in the cache).
func ObserveMaintenanceJob(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	od *odoov1.OdooDeployment,
) (JobObservation, error) {
	current := od.Status.CurrentInitJob
	namespace := current.Namespace
	if namespace == "" {
		namespace = od.Namespace
	}
	job := &batchv1.Job{}
	err := c.Get(ctx, client.ObjectKey{Name: current.Name, Namespace: namespace}, job)
	if errors.IsNotFound(err) {
		return JobObservation{State: JobStateMissing}, nil
	}
	if err != nil {
		return JobObservation{}, fmt.Errorf("get job %s: %w", current.Name, err)
	}

	if job.Status.Succeeded > 0 || jobCondition(job, batchv1.JobComplete) != nil {
		return JobObservation{State: JobStateSucceeded, Job: job}, nil
	}
	if failed := jobCondition(job, batchv1.JobFailed); failed != nil {
		return JobObservation{
			State:          JobStateFailed,
			Job:            job,
			FailureMessage: strings.TrimSpace(failed.Reason + ": " + failed.Message),
			QuotaMessage:   quotaMessage(ctx, reader, job),
		}, nil
	}
	obs := JobObservation{State: JobStateActive, Job: job}
	if job.Status.Active == 0 {
		// No pod yet: the only interesting reason is a ResourceQuota rejection.
		obs.QuotaMessage = quotaMessage(ctx, reader, job)
	}
	return obs, nil
}

// quotaMessage returns the message of a FailedCreate event mentioning an
// exceeded quota for the Job, or "" (best effort).
func quotaMessage(ctx context.Context, reader client.Reader, job *batchv1.Job) string {
	if reader == nil {
		return ""
	}
	events := &corev1.EventList{}
	err := reader.List(ctx, events,
		client.InNamespace(job.Namespace),
		client.MatchingFields{
			"involvedObject.kind": "Job",
			"involvedObject.name": job.Name,
			"reason":              "FailedCreate",
		},
	)
	if err != nil {
		log.FromContext(ctx).V(1).Info("could not list events for job", "job", job.Name, "error", err.Error())
		return ""
	}
	var latest *corev1.Event
	for i := range events.Items {
		e := &events.Items[i]
		if !strings.Contains(e.Message, "exceeded quota") {
			continue
		}
		if latest == nil || e.LastTimestamp.After(latest.LastTimestamp.Time) {
			latest = e
		}
	}
	if latest == nil {
		return ""
	}
	return latest.Message
}

// EnsureMaintenanceJob creates the Job that installs/upgrades modules, or
// adopts the one with the same name that a previous reconcile already created.
func EnsureMaintenanceJob(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
	initModules, upgradeModules []string,
	firstBoot bool,
) (*batchv1.Job, error) {
	logger := log.FromContext(ctx)
	job := od.GetMaintenanceJobTemplate(initModules, upgradeModules, firstBoot)
	if err := controllerutil.SetControllerReference(od, &job, scheme); err != nil {
		return nil, err
	}
	err := c.Create(ctx, &job)
	if errors.IsAlreadyExists(err) {
		existing := &batchv1.Job{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(&job), existing); err != nil {
			return nil, fmt.Errorf("get existing job %s: %w", job.Name, err)
		}
		logger.Info("Adopting existing maintenance job", "job", job.Name)
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create job %s: %w", job.Name, err)
	}
	logger.Info("Created maintenance job", "job", job.Name, "init", initModules, "upgrade", upgradeModules)
	return &job, nil
}

// DeleteMaintenanceJob deletes a finished Job and its pods (background propagation).
func DeleteMaintenanceJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	propagation := metav1.DeletePropagationBackground
	err := c.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete job %s: %w", job.Name, err)
	}
	return nil
}
