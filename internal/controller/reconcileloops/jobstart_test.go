package reconcileloops

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func waiting(name, reason, message string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message},
	}}
}

// pendingPod is pod "p", pending on node-a.
func pendingPod(scheduled *corev1.PodCondition, init, main []corev1.ContainerStatus) corev1.Pod {
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Spec: corev1.PodSpec{NodeName: "node-a"}}
	p.Status.Phase = corev1.PodPending
	if scheduled != nil {
		p.Status.Conditions = []corev1.PodCondition{*scheduled}
	}
	p.Status.InitContainerStatuses, p.Status.ContainerStatuses = init, main
	return p
}

func TestPodStartMessage(t *testing.T) {
	now := time.Now()
	unschedulable := &corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
		Message: "0/3 nodes are available: 3 Insufficient memory."}
	scheduledAt := func(ago time.Duration) *corev1.PodCondition {
		return &corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(now.Add(-ago))}
	}
	running := corev1.ContainerStatus{Name: "backup", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	cases := map[string]struct {
		pods []corev1.Pod
		want string
	}{
		"no pods": {nil, ""},
		"unschedulable": {[]corev1.Pod{pendingPod(unschedulable, nil, nil)},
			"pod p cannot be scheduled: Unschedulable: 0/3 nodes are available: 3 Insufficient memory."},
		"a missing Secret": {[]corev1.Pod{pendingPod(scheduledAt(time.Minute), nil,
			[]corev1.ContainerStatus{waiting("backup", "CreateContainerConfigError", `secret "restic" not found`)})},
			`container backup of pod p cannot start: CreateContainerConfigError: secret "restic" not found`},
		"a step that cannot be created": {[]corev1.Pod{pendingPod(scheduledAt(time.Minute),
			[]corev1.ContainerStatus{waiting("load", "CreateContainerError", "")}, []corev1.ContainerStatus{waiting("backup", "PodInitializing", "")})},
			"container load of pod p cannot start: CreateContainerError"},
		"creating for a short while": {[]corev1.Pod{pendingPod(scheduledAt(time.Minute), nil,
			[]corev1.ContainerStatus{waiting("backup", "ContainerCreating", "")})}, ""},
		"creating for long": {[]corev1.Pod{pendingPod(scheduledAt(12*time.Minute), nil,
			[]corev1.ContainerStatus{waiting("backup", "ContainerCreating", "")})},
			"pod p has waited 12m0s on node node-a for its first container to start: a volume it mounts may be attached"},
		"pulling for long is the pull's to report": {[]corev1.Pod{pendingPod(scheduledAt(12*time.Minute), nil,
			[]corev1.ContainerStatus{waiting("backup", "ImagePullBackOff", "")})}, ""},
		"a step that ran, then one that cannot be created": {[]corev1.Pod{pendingPod(scheduledAt(time.Hour),
			[]corev1.ContainerStatus{{Name: "load", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
				waiting("odoo", "CreateContainerConfigError", `secret "env" not found`)},
			[]corev1.ContainerStatus{waiting("backup", "PodInitializing", "")})},
			`container odoo of pod p cannot start: CreateContainerConfigError: secret "env" not found`},
		"a step that ran, then a long wait": {[]corev1.Pod{pendingPod(scheduledAt(time.Hour),
			[]corev1.ContainerStatus{{Name: "load", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
				waiting("odoo", "PodInitializing", "")}, nil)}, ""},
		"a running pod": {[]corev1.Pod{func() corev1.Pod {
			p := pendingPod(unschedulable, nil, []corev1.ContainerStatus{running})
			p.Status.Phase = corev1.PodRunning
			return p
		}()}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := podStartMessage(tc.pods, now)
			if (tc.want == "") != (got == "") || !strings.HasPrefix(got, tc.want) {
				t.Errorf("podStartMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJobNeverStarted(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Hour))
	job := func(active, failed, succeeded int32) *batchv1.Job {
		j := &batchv1.Job{}
		j.Status.StartTime = &started
		j.Status.Active, j.Status.Failed, j.Status.Succeeded = active, failed, succeeded
		return j
	}
	pending := pendingPod(nil, nil, []corev1.ContainerStatus{waiting("backup", "ContainerCreating", "")})
	ran := pendingPod(nil, nil, []corev1.ContainerStatus{{Name: "backup",
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}})
	noState := pendingPod(nil, nil, []corev1.ContainerStatus{{Name: "backup"}})
	cases := map[string]struct {
		job  *batchv1.Job
		pods []corev1.Pod
		want bool
	}{
		"no pod yet (a quota)":                 {job(0, 0, 0), nil, true},
		"a pending pod":                        {job(1, 0, 0), []corev1.Pod{pending}, true},
		"an active pod that cannot be seen":    {job(1, 0, 0), nil, false},
		"a container that ran before":          {job(1, 0, 0), []corev1.Pod{ran}, false},
		"a container status that says nothing": {job(1, 0, 0), []corev1.Pod{noState}, false},
		"an earlier pod failed":                {job(1, 1, 0), []corev1.Pod{pending}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, since := JobNeverStarted(tc.job, tc.pods)
			if got != tc.want || !since.Equal(started.Time) {
				t.Errorf("JobNeverStarted = %v since %v, want %v", got, since, tc.want)
			}
		})
	}
}
