package reconcileloops

import (
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// SlowContainerCreating is how long a scheduled pod may wait for its first
// container before its wait is reported: past it, a volume the pod mounts is
// most likely attached to another node or failing to mount.
const SlowContainerCreating = 5 * time.Minute

// containerStartReasons are the kubelet's waiting reasons for a container it
// tried to create or start and could not: a Secret or ConfigMap the
// container's env or volumes name is missing (CreateContainerConfigError), or
// the runtime refused it. The kubelet keeps retrying, so the Job never fails
// on its own before its deadline.
var containerStartReasons = map[string]bool{
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"PreCreateHookError":         true,
	"PostStartHookError":         true,
}

// PodRanAnyContainer reports whether any container of the pod, a step
// (init container) or the main one, started or may have: only a pod whose
// every container status shows it waiting, and never ran before, says no.
func PodRanAnyContainer(p *corev1.Pod) bool {
	if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodUnknown {
		return true
	}
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if cs.State.Waiting == nil || cs.LastTerminationState.Terminated != nil || cs.LastTerminationState.Running != nil {
				return true
			}
		}
	}
	return false
}

// JobNeverStarted reports whether no pod of an unfinished Job has ever run a
// container: none counted as failed or succeeded, every pod the Job counts
// as active is among pods, and every pod there is (if any) is pending with
// no container that ran. Since is when the Job started (or was created).
func JobNeverStarted(job *batchv1.Job, pods []corev1.Pod) (bool, time.Time) {
	since := job.CreationTimestamp.Time
	if job.Status.StartTime != nil {
		since = job.Status.StartTime.Time
	}
	if job.Status.Failed > 0 || job.Status.Succeeded > 0 || len(pods) < int(job.Status.Active) {
		return false, since
	}
	for i := range pods {
		if PodRanAnyContainer(&pods[i]) {
			return false, since
		}
	}
	return true, since
}

// podStartMessage says why one of the pending pods cannot go on, other than
// an image pull (imagePullMessage): it cannot be scheduled, a container (a
// step or the main one) cannot be created (a missing Secret, say), or it has
// waited for its first container for longer than SlowContainerCreating since
// it was scheduled. It is "" when nothing is wrong that the pods' status
// shows.
func podStartMessage(pods []corev1.Pod, now time.Time) string {
	for i := range pods {
		p := &pods[i]
		if !p.DeletionTimestamp.IsZero() || p.Status.Phase != corev1.PodPending {
			continue
		}
		var scheduled *corev1.PodCondition
		for j := range p.Status.Conditions {
			if c := &p.Status.Conditions[j]; c.Type == corev1.PodScheduled {
				scheduled = c
			}
		}
		if scheduled != nil && scheduled.Status == corev1.ConditionFalse {
			return joinText(fmt.Sprintf("pod %s cannot be scheduled: %s", p.Name, orText(scheduled.Reason, "Unschedulable")),
				scheduled.Message)
		}
		statuses := append(append([]corev1.ContainerStatus(nil), p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
		for _, cs := range statuses {
			if w := cs.State.Waiting; w != nil && containerStartReasons[w.Reason] {
				return joinText(fmt.Sprintf("container %s of pod %s cannot start: %s", cs.Name, p.Name, w.Reason), w.Message)
			}
		}
		if scheduled != nil && scheduled.Status == corev1.ConditionTrue && !scheduled.LastTransitionTime.IsZero() &&
			now.Sub(scheduled.LastTransitionTime.Time) > SlowContainerCreating && !waitsForImage(statuses) &&
			!PodRanAnyContainer(p) {
			node := ""
			if p.Spec.NodeName != "" {
				node = " on node " + p.Spec.NodeName
			}
			return fmt.Sprintf("pod %s has waited %s%s for its first container to start: a volume it mounts may be "+
				"attached to another node or failing to mount (kubectl describe pod %s shows why)",
				p.Name, now.Sub(scheduled.LastTransitionTime.Time).Round(time.Minute), node, p.Name)
		}
	}
	return ""
}

// waitsForImage reports whether a container waits on an image pull, which
// imagePullMessage reports.
func waitsForImage(statuses []corev1.ContainerStatus) bool {
	for _, cs := range statuses {
		if w := cs.State.Waiting; w != nil && imagePullWaitingReasons[w.Reason] {
			return true
		}
	}
	return false
}

func joinText(a, b string) string {
	if b = strings.TrimSpace(b); b == "" {
		return a
	}
	return a + ": " + b
}

func orText(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
