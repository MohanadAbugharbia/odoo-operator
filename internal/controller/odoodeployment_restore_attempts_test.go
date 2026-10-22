package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// podStatus is what the kubelet reported for one attempt (pod) of a
// restore Job: its steps (init containers) and its main container.
type podStatus struct {
	phase corev1.PodPhase
	steps []corev1.ContainerStatus
	main  *corev1.ContainerStatus
}

// attemptPod creates a pod of Job job with the restore Job's steps (load,
// odoo) and main container, and sets its status. Pods are told apart by
// their creation time, which has a resolution of one second: a test that
// creates several waits between them (see nextSecond).
func (h *backupHarness) attemptPod(job, name string, st podStatus) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: job + "-" + name, Namespace: ns,
			Labels: map[string]string{"job-name": job, "batch.kubernetes.io/job-name": job},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			InitContainers: []corev1.Container{
				{Name: "load", Image: "backup"}, {Name: "odoo", Image: "odoo"},
			},
			Containers: []corev1.Container{{Name: jobspec.ContainerName, Image: "backup"}},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)) })
	pod.Status.Phase = st.phase
	pod.Status.InitContainerStatuses = st.steps
	if st.main != nil {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{*st.main}
	}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pod)).To(Succeed())
	return pod
}

// nextSecond waits until a pod created now gets a later creation time than
// the ones created so far.
func nextSecond() {
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 10*time.Millisecond)))
}

func stepDone(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Image: "x", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", FinishedAt: metav1.Now()},
	}}
}

func stepFailed(name, reason string, exit int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Image: "x", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Reason: reason, FinishedAt: metav1.Now()},
	}}
}

func stepWaiting(name, reason, message string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Image: "x", State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message},
	}}
}

func mainWaiting() *corev1.ContainerStatus {
	s := stepWaiting(jobspec.ContainerName, "PodInitializing", "")
	return &s
}

func mainRunning() *corev1.ContainerStatus {
	return &corev1.ContainerStatus{Name: jobspec.ContainerName, Image: "x", State: corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
	}}
}

func mainResult(res result.Result) *corev1.ContainerStatus {
	exit := int32(0)
	if !res.OK {
		exit = 1
	}
	return &corev1.ContainerStatus{Name: jobspec.ContainerName, Image: "x", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exit, Reason: "Completed", Message: string(result.Encode(res)), FinishedAt: metav1.Now(),
		},
	}}
}

// endJob marks Job name failed (or succeeded) after the given number of
// failed pods, as the Job controller does.
func (h *backupHarness) endJob(name string, failed int32, succeeded bool) {
	job := h.job(name)
	ExpectWithOffset(1, job).NotTo(BeNil())
	if succeeded {
		markJobSucceeded(job)
	} else {
		markJobFailed(job)
	}
	job.Status.Failed = failed
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, job)).To(Succeed())
}

var _ = Describe("Restore attempts", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	// startRestore creates a restore of the completed backup and returns
	// its Job.
	startRestore := func() string {
		h = newBackupHarness()
		h.completedBackup()
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		Expect(job).NotTo(BeEmpty())
		return job
	}

	It("reads the newest pod, not an older one that finished", func() {
		job := startRestore()
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")},
			main: mainResult(result.Result{OK: false, Step: result.StepCheck, Error: "table x has 1 rows instead of 2"}),
		})
		nextSecond()
		h.attemptPod(job, "b", podStatus{
			phase: corev1.PodRunning, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")},
			main: mainRunning(),
		})
		h.endJob(job, 1, false)
		h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring),
			"the newest attempt may have been swapping when the Job ended: %s", rs.Status.Message)
		Expect(rs.Status.LiveChanged).To(BeTrue())
		Expect(rs.Status.Message).NotTo(ContainSubstring("table x"), "the older attempt's result is stale")
		Expect(h.replicas()).To(BeZero())
	})

	It("fails cleanly when a step before the swap failed in every attempt", func() {
		job := startRestore()
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepDone("load"), stepFailed("odoo", "Error", 1)},
			main: mainWaiting(),
		})
		nextSecond()
		h.attemptPod(job, "b", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepFailed("load", "OOMKilled", 137), stepWaiting("odoo", "PodInitializing", "")},
			main: mainWaiting(),
		})
		h.endJob(job, 2, false)
		_, od := h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed), rs.Status.Message)
		Expect(rs.Status.LiveChanged).To(BeFalse())
		Expect(rs.Status.RetryAfter).To(BeNil())
		Expect(rs.Status.Message).To(ContainSubstring("failed in step load: OOMKilled (exit code 137)"), "the newest attempt's reason")
		Expect(rs.Status.Message).NotTo(ContainSubstring("step odoo"))
		Expect(rs.Status.Message).To(ContainSubstring("BackoffLimitExceeded"))
		Expect(rs.Status.Message).To(ContainSubstring("no pod of it got past the steps before the swap"))
		Expect(rs.Status.Message).To(ContainSubstring("the live data was not changed"))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).NotTo(Equal(ReasonRestoreIncomplete))

		By("the half-restored copy is cleaned up and Odoo comes back")
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationCleanup))
		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true})
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(h.replicas()).To(Equal(int32(1)))
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
	})

	It("fails cleanly when a step's image cannot be pulled before the deadline", func() {
		job := startRestore()
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodPending,
			steps: []corev1.ContainerStatus{stepDone("load"), stepWaiting("odoo", "ImagePullBackOff", `Back-off pulling image "odoo:gone"`)},
			main:  mainWaiting(),
		})
		h.endJob(job, 1, false)
		h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed), rs.Status.Message)
		Expect(rs.Status.Message).To(ContainSubstring(`step odoo could not start: ImagePullBackOff: Back-off pulling image "odoo:gone"`))
		Expect(rs.Status.LiveChanged).To(BeFalse())
	})

	It("fails cleanly when the Job ended before any pod ran", func() {
		job := startRestore()
		h.endJob(job, 0, false)
		h.reconcile()
		Expect(h.restore("r").Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
	})

	It("keeps going forward when an older attempt reached the swap step", func() {
		job := startRestore()
		killed := &corev1.ContainerStatus{Name: jobspec.ContainerName, Image: "x", State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", FinishedAt: metav1.Now()},
		}}
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")}, main: killed,
		})
		nextSecond()
		h.attemptPod(job, "b", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepFailed("load", "Error", 1), stepWaiting("odoo", "PodInitializing", "")},
			main: mainWaiting(),
		})
		h.endJob(job, 2, false)
		h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
		Expect(rs.Status.LiveChanged).To(BeTrue())
		Expect(rs.Status.LiveChangeUnconfirmed).To(BeTrue())
		Expect(rs.Status.Message).To(ContainSubstring("failed in step load: Error (exit code 1)"))
		Expect(rs.Status.Message).To(ContainSubstring("Odoo stays stopped"))
		Expect(h.replicas()).To(BeZero())

		By("a next attempt that again stops before the swap step settles nothing")
		past := metav1.NewTime(time.Now().Add(-time.Second))
		rs.Status.RetryAfter = &past
		Expect(k8sClient.Status().Update(ctx, rs)).To(Succeed())
		_, od := h.reconcile()
		second := od.Status.CurrentOperation.Job
		Expect(second).NotTo(BeEmpty())
		Expect(second).NotTo(Equal(job))
		h.attemptPod(second, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepFailed("load", "OOMKilled", 137)}, main: mainWaiting(),
		})
		h.endJob(second, 1, false)
		h.reconcile()
		rs = h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
		Expect(rs.Status.LiveChanged).To(BeTrue())
		Expect(rs.Status.LiveChangeUnconfirmed).To(BeTrue())
	})

	It("keeps going forward when pods the Job counted are gone", func() {
		job := startRestore()
		h.attemptPod(job, "b", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepFailed("load", "OOMKilled", 137)}, main: mainWaiting(),
		})
		h.endJob(job, 3, false)
		h.reconcile()
		rs := h.restore("r")
		Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring), "two attempts left no pod to tell how far they got")
		Expect(rs.Status.LiveChanged).To(BeTrue())
	})

	It("abandons a force-deleted restore whose pods never reached the swap step", func() {
		job := startRestore()
		fresh := h.restore("r")
		controllerutil.RemoveFinalizer(fresh, odoov1.RestoreFinalizer)
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fresh)).To(Succeed())
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepFailed("load", "Error", 1)}, main: mainWaiting(),
		})
		h.endJob(job, 1, false)
		_, od := h.reconcile()
		Expect(od.Status.Phase).NotTo(Equal(odoov1.PhaseFailed))
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
	})

	It("takes the result of the newest pod when it succeeded after a failed one", func() {
		job := startRestore()
		h.attemptPod(job, "a", podStatus{
			phase: corev1.PodFailed, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")},
			main: mainResult(result.Result{OK: false, Step: result.StepSwap, Error: "rename failed", LiveChanged: true}),
		})
		nextSecond()
		h.attemptPod(job, "b", podStatus{
			phase: corev1.PodSucceeded, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")},
			main: mainResult(result.Result{OK: true, LiveChanged: true, PreviousKept: true, Image: "odoo:a"}),
		})
		h.endJob(job, 1, true)
		h.reconcile()
		Expect(h.restore("r").Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
	})
})

var _ = Describe("Deleting an instance during a restore", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	It("waits for the restore Job's pod when the database is retained", func() {
		h = newBackupHarness(func(od *odoov1.OdooDeployment) {
			od.Spec.Database.DeletionPolicy = odoov1.DeletionPolicyRetain
		})
		h.completedBackup()
		Expect(h.get().Finalizers).To(BeEmpty(), "nothing runs, nothing to hold")
		h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		Expect(job).NotTo(BeEmpty())
		Expect(od.Finalizers).To(ConsistOf(odoov1.OperationsFinalizer))
		pod := h.attemptPod(job, "a", podStatus{
			phase: corev1.PodRunning, steps: []corev1.ContainerStatus{stepDone("load"), stepDone("odoo")}, main: mainRunning(),
		})
		pod.Labels[odoov1.LabelOdooDeployment] = h.name
		Expect(k8sClient.Update(ctx, pod)).To(Succeed())

		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		for i := 0; i < 2; i++ {
			_, od = h.reconcile()
			Expect(od).NotTo(BeNil(), "the swap is not cut short")
		}
		Expect(h.job(job)).NotTo(BeNil())
		Expect(h.fake.CallCount("Drop")).To(BeZero())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{*mainResult(result.Result{OK: true, LiveChanged: true})}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		_, od = h.reconcile()
		Expect(od).To(BeNil())
		Expect(h.fake.CallCount("Drop")).To(BeZero())
		Expect(h.fake.Has(h.dbName())).To(BeTrue())
	})
})
