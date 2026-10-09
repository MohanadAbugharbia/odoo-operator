package reconcileloops

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// labelJobName is set by the Job controller on every pod it creates.
const labelJobName = "batch.kubernetes.io/job-name"

// imagePullWaitingReasons are the kubelet's container waiting reasons for an
// image it cannot pull. Every one of them leaves the pod Pending indefinitely
// (the kubelet keeps retrying), so the Job never fails on its own.
var imagePullWaitingReasons = map[string]bool{
	"ErrImagePull":        true,
	"ImagePullBackOff":    true,
	"InvalidImageName":    true,
	"ErrImageNeverPull":   true,
	"RegistryUnavailable": true,
}

// imagePullMessage returns "<reason>: <message>" for the first container of
// one of the Job's pods that is waiting on an image pull, or "" (best effort).
func imagePullMessage(ctx context.Context, reader client.Reader, job *batchv1.Job) string {
	if reader == nil {
		return ""
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(job.Namespace), client.MatchingLabels{labelJobName: job.Name}); err != nil {
		log.FromContext(ctx).V(1).Info("could not list pods for job", "job", job.Name, "error", err.Error())
		return ""
	}
	for i := range pods.Items {
		statuses := append([]corev1.ContainerStatus{}, pods.Items[i].Status.InitContainerStatuses...)
		statuses = append(statuses, pods.Items[i].Status.ContainerStatuses...)
		for _, cs := range statuses {
			if w := cs.State.Waiting; w != nil && imagePullWaitingReasons[w.Reason] {
				return strings.TrimSpace(w.Reason + ": " + w.Message)
			}
		}
	}
	return ""
}

// ImageCheck is the outcome of CheckImage.
type ImageCheck struct {
	// Pulled is true once spec.image has been pulled and ran `--version`.
	Pulled bool
	// PullMessage is set while the kubelet cannot pull the image.
	PullMessage string
	// FailureMessage is set when the image pulled but the check failed.
	FailureMessage string
	// QuotaMessage is set when a ResourceQuota keeps the check's pod from
	// being created.
	QuotaMessage string
	// JobName is the image check Job for spec.image.
	JobName string
}

// CheckImage proves that spec.image can be pulled before anything is scaled
// down for it, by running a short Job on it. The Job is created on the first
// call and observed on the next ones; Pulled turns true once it succeeded.
func CheckImage(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	scheme *runtime.Scheme,
	od *odoov1.OdooDeployment,
) (ImageCheck, error) {
	desired := od.GetImageCheckJobTemplate()
	check := ImageCheck{JobName: desired.Name}
	if err := PruneImageCheckJobs(ctx, c, od, desired.Name); err != nil {
		return check, err
	}

	job := &batchv1.Job{}
	err := c.Get(ctx, client.ObjectKeyFromObject(&desired), job)
	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(od, &desired, scheme); err != nil {
			return check, err
		}
		if err := c.Create(ctx, &desired); err != nil && !errors.IsAlreadyExists(err) {
			return check, fmt.Errorf("create image check job %s: %w", desired.Name, err)
		}
		log.FromContext(ctx).Info("Created image check job", "job", desired.Name, "image", od.Spec.Image)
		return check, nil
	}
	if err != nil {
		return check, fmt.Errorf("get image check job %s: %w", desired.Name, err)
	}

	switch {
	case job.Status.Succeeded > 0 || jobCondition(job, batchv1.JobComplete) != nil:
		check.Pulled = true
	case jobCondition(job, batchv1.JobFailed) != nil:
		failed := jobCondition(job, batchv1.JobFailed)
		check.FailureMessage = strings.TrimSpace(failed.Reason + ": " + failed.Message)
	default:
		check.PullMessage = imagePullMessage(ctx, reader, job)
		if check.PullMessage == "" && job.Status.Active == 0 {
			check.QuotaMessage = quotaMessage(ctx, reader, job)
		}
	}
	return check, nil
}

// PruneImageCheckJobs deletes the OdooDeployment's image check Jobs except
// keep ("" deletes them all): checks for an image spec.image has moved away
// from, and finished ones once their image is applied.
func PruneImageCheckJobs(ctx context.Context, c client.Client, od *odoov1.OdooDeployment, keep string) error {
	jobs := &batchv1.JobList{}
	err := c.List(ctx, jobs, client.InNamespace(od.Namespace), client.MatchingLabels{
		odoov1.LabelOdooDeployment: od.Name,
		odoov1.LabelJobKind:        odoov1.JobKindImageCheck,
	})
	if err != nil {
		return fmt.Errorf("list image check jobs: %w", err)
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if job.Name == keep || !job.DeletionTimestamp.IsZero() {
			continue
		}
		if err := DeleteMaintenanceJob(ctx, c, job); err != nil {
			return err
		}
	}
	return nil
}
