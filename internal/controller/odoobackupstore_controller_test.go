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

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
)

var backupWorkCounter int64

// bwName returns a name no other test in this suite uses.
func bwName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&backupWorkCounter, 1))
}

// bwTracker remembers what a test created and deletes it afterwards,
// releasing finalizers first.
type bwTracker struct{ objs []client.Object }

func (t *bwTracker) create(obj client.Object) {
	ExpectWithOffset(1, k8sClient.Create(ctx, obj)).To(Succeed())
	t.objs = append(t.objs, obj)
}

func (t *bwTracker) track(obj client.Object) { t.objs = append(t.objs, obj) }

func (t *bwTracker) cleanup() {
	for i := len(t.objs) - 1; i >= 0; i-- {
		obj := t.objs[i]
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			continue
		}
		if len(obj.GetFinalizers()) > 0 {
			obj.SetFinalizers(nil)
			_ = k8sClient.Update(ctx, obj)
		}
		propagation := metav1.DeletePropagationBackground
		_ = k8sClient.Delete(ctx, obj, &client.DeleteOptions{PropagationPolicy: &propagation})
	}
	t.objs = nil
}

// ---- OdooBackupStore ---------------------------------------------------------

type storeHarness struct {
	t        *bwTracker
	name     string
	key      types.NamespacedName
	recorder *record.FakeRecorder
	imageErr error
	clock    time.Duration // offset added to the real time
	r        *OdooBackupStoreReconciler
}

func newStoreHarness(t *bwTracker) *storeHarness {
	name := bwName("bs")
	s := &storeHarness{
		t:        t,
		name:     name,
		key:      types.NamespacedName{Namespace: ns, Name: name},
		recorder: record.NewFakeRecorder(100),
	}
	s.r = &OdooBackupStoreReconciler{
		Client:    k8sClient,
		Scheme:    k8sClient.Scheme(),
		APIReader: k8sClient,
		Recorder:  s.recorder,
		Image: func(context.Context) (string, error) {
			if s.imageErr != nil {
				return "", s.imageErr
			}
			return "backup:test", nil
		},
		Now: func() time.Time { return time.Now().Add(s.clock) },
	}
	return s
}

// events returns the events recorded since the last call.
func (s *storeHarness) events() []string {
	var out []string
	for {
		select {
		case e := <-s.recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func (s *storeHarness) keySecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: s.name + "-key", Namespace: ns},
		Data:       map[string][]byte{"password": []byte("restic-password")},
	}
}

func (s *storeHarness) pvcStore() *odoov1.OdooBackupStore {
	return &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			PVC: &odoov1.PVCStore{ClaimName: "backups"},
			EncryptionKeySecret: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: s.name + "-key"}, Key: "password",
			},
		},
	}
}

// createPVCStore creates the encryption key Secret and a PVC store.
func (s *storeHarness) createPVCStore() {
	s.t.create(s.keySecret())
	s.t.create(s.pvcStore())
}

func (s *storeHarness) reconcile() ctrl.Result {
	res, err := s.r.Reconcile(ctx, ctrl.Request{NamespacedName: s.key})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return res
}

func (s *storeHarness) get() *odoov1.OdooBackupStore {
	store := &odoov1.OdooBackupStore{}
	ExpectWithOffset(1, k8sClient.Get(ctx, s.key, store)).To(Succeed())
	return store
}

func (s *storeHarness) ready() *metav1.Condition {
	return meta.FindStatusCondition(s.get().Status.Conditions, odoov1.ConditionReady)
}

func (s *storeHarness) jobs() []batchv1.Job {
	list := &batchv1.JobList{}
	ExpectWithOffset(1, k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{jobspec.LabelStore: s.name})).To(Succeed())
	var live []batchv1.Job
	for _, j := range list.Items {
		if j.DeletionTimestamp.IsZero() {
			live = append(live, j)
		}
	}
	return live
}

func (s *storeHarness) job(name string) *batchv1.Job {
	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, job)
	if apierrors.IsNotFound(err) {
		return nil
	}
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return job
}

// finishJob marks the Job succeeded or failed, as the Job controller would.
func (s *storeHarness) finishJob(name string, ok bool) {
	job := s.job(name)
	ExpectWithOffset(1, job).NotTo(BeNil())
	if ok {
		markJobSucceeded(job)
	} else {
		markJobFailed(job)
	}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, job)).To(Succeed())
	s.t.track(job)
}

// podReporting creates the Job's pod whose backup container terminated
// with the given termination message.
func (s *storeHarness) podReporting(jobName, message string, exitCode int32) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: jobName + "-pod", Namespace: ns,
			Labels: map[string]string{"job-name": jobName, "batch.kubernetes.io/job-name": jobName},
		},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: jobspec.ContainerName, Image: "backup:test"}}},
	}
	s.t.create(pod)
	now := metav1.Now()
	phase := corev1.PodSucceeded
	if exitCode != 0 {
		phase = corev1.PodFailed
	}
	pod.Status.Phase = phase
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  jobspec.ContainerName,
		Image: "backup:test",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exitCode, Reason: "Completed", Message: message, StartedAt: now, FinishedAt: now,
		}},
	}}
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

var _ = Describe("OdooBackupStore reconciler", func() {
	var t *bwTracker
	var s *storeHarness

	BeforeEach(func() {
		t = &bwTracker{}
		s = newStoreHarness(t)
	})
	AfterEach(func() {
		for _, j := range s.jobs() {
			t.track(j.DeepCopy())
		}
		t.cleanup()
	})

	It("starts one probe Job owned by the store and reports Probing", func() {
		s.createPVCStore()
		res := s.reconcile()
		Expect(res.RequeueAfter).To(Equal(10 * time.Second))

		store := s.get()
		Expect(store.Status.ProbeJob).NotTo(BeEmpty())
		Expect(store.Status.ProbedHash).NotTo(BeEmpty())
		Expect(store.Status.Ready).To(BeFalse())
		Expect(store.Status.ObservedGeneration).To(Equal(store.Generation))
		cond := meta.FindStatusCondition(store.Status.Conditions, odoov1.ConditionReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(ReasonStoreProbing))
		Expect(cond.Message).To(ContainSubstring(store.Status.ProbeJob))

		job := s.job(store.Status.ProbeJob)
		Expect(job).NotTo(BeNil())
		owner := metav1.GetControllerOf(job)
		Expect(owner).NotTo(BeNil())
		Expect(owner.Kind).To(Equal("OdooBackupStore"))
		Expect(owner.UID).To(Equal(store.UID))
		Expect(job.Labels).To(HaveKeyWithValue(jobspec.LabelStore, s.name))
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal(jobspec.ContainerName))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("backup:test"))
		Expect(job.Spec.Template.Spec.AutomountServiceAccountToken).To(HaveValue(BeFalse()))

		By("reconciling again while the Job runs")
		res = s.reconcile()
		Expect(res.RequeueAfter).To(Equal(10 * time.Second))
		Expect(s.jobs()).To(HaveLen(1))
		Expect(s.get().Status.ProbeJob).To(Equal(job.Name))
		Expect(s.events()).To(BeEmpty())
	})

	It("marks the store reachable when the probe succeeds and deletes the Job", func() {
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		s.finishJob(jobName, true)
		s.podReporting(jobName, `{"ok":true}`, 0)

		res := s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", StoreProbeInterval))

		store := s.get()
		Expect(store.Status.Ready).To(BeTrue())
		Expect(store.Status.ProbeJob).To(BeEmpty())
		Expect(store.Status.ProbedHash).NotTo(BeEmpty())
		Expect(store.Status.LastProbeTime).NotTo(BeNil())
		cond := meta.FindStatusCondition(store.Status.Conditions, odoov1.ConditionReady)
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(ReasonStoreReachable))
		Eventually(func() *batchv1.Job { return s.job(jobName) }).Should(BeNil())
		Expect(s.events()).To(BeEmpty())

		By("reconciling again within the probe period")
		res = s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", StoreProbeInterval))
		Expect(s.jobs()).To(BeEmpty())
		Expect(s.get().Status.Ready).To(BeTrue())
	})

	It("probes again in the next period, measured on its own clock", func() {
		s.clock = 48 * time.Hour
		s.createPVCStore()
		s.reconcile()
		first := s.get().Status.ProbeJob
		s.finishJob(first, true)
		s.podReporting(first, `{"ok":true}`, 0)
		res := s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", StoreProbeInterval))
		hash := s.get().Status.ProbedHash

		s.clock += StoreProbeInterval
		s.reconcile()
		store := s.get()
		Expect(store.Status.ProbedHash).NotTo(Equal(hash))
		Expect(store.Status.ProbeJob).NotTo(BeEmpty())
		Expect(store.Status.ProbeJob).NotTo(Equal(first))
		Expect(store.Status.Ready).To(BeTrue(), "the last probe's verdict stands while the next runs")
		Expect(s.jobs()).To(HaveLen(1))
	})

	It("reports a failed probe with its error and warns once", func() {
		// Start ten minutes into a probe period, so no step of this spec
		// crosses a period boundary by accident.
		base := time.Now()
		period := int64(StoreProbeInterval / time.Second)
		s.clock = time.Unix((base.Unix()/period+1)*period, 0).Add(10 * time.Minute).Sub(base)
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		s.finishJob(jobName, false)
		s.podReporting(jobName, `{"ok":false,"step":"probe","error":"cannot write"}`, 1)

		res := s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", StoreRetryInterval), "a failed probe is retried soon")
		store := s.get()
		Expect(store.Status.Ready).To(BeFalse())
		Expect(store.Status.ProbeJob).To(BeEmpty())
		Expect(store.Status.LastProbeTime).NotTo(BeNil())
		cond := meta.FindStatusCondition(store.Status.Conditions, odoov1.ConditionReady)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(ReasonStoreUnreachable))
		Expect(cond.Message).To(ContainSubstring("failed at step probe: cannot write"))
		Expect(cond.Message).To(ContainSubstring("kubectl -n " + ns + " logs job/" + jobName))
		Expect(s.job(jobName)).NotTo(BeNil(), "a failed probe Job is kept for its logs")

		events := s.events()
		Expect(events).To(HaveLen(1))
		Expect(events[0]).To(HavePrefix("Warning " + ReasonStoreUnreachable))
		Expect(events[0]).To(ContainSubstring("cannot write"))

		By("reconciling again")
		s.reconcile()
		Expect(s.events()).To(BeEmpty())
		Expect(s.jobs()).To(HaveLen(1))

		By("retrying after StoreRetryInterval, which replaces the failed Job")
		s.clock += StoreRetryInterval
		s.reconcile()
		retry := s.get().Status.ProbeJob
		Expect(retry).NotTo(BeEmpty())
		Expect(retry).NotTo(Equal(jobName))
		Eventually(func() *batchv1.Job { return s.job(jobName) }).Should(BeNil())
		Expect(s.jobs()).To(HaveLen(1))

		By("failing the same way again, which is not reported twice")
		s.finishJob(retry, false)
		s.podReporting(retry, `{"ok":false,"step":"probe","error":"cannot write"}`, 1)
		res = s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically("<=", StoreRetryInterval))
		store = s.get()
		Expect(store.Status.Ready).To(BeFalse())
		Expect(s.ready().Message).To(ContainSubstring("logs job/" + retry))
		Expect(s.events()).To(BeEmpty())

		By("failing differently, which is reported")
		s.clock += StoreRetryInterval
		s.reconcile()
		third := s.get().Status.ProbeJob
		Expect(third).NotTo(BeEmpty())
		s.finishJob(third, false)
		s.podReporting(third, `{"ok":false,"step":"probe","error":"cannot read"}`, 1)
		s.reconcile()
		events = s.events()
		Expect(events).To(HaveLen(1))
		Expect(events[0]).To(ContainSubstring("cannot read"))

		By("succeeding on a later retry, which goes back to the long period")
		s.clock += StoreRetryInterval
		s.reconcile()
		fourth := s.get().Status.ProbeJob
		Expect(fourth).NotTo(BeEmpty())
		s.finishJob(fourth, true)
		s.podReporting(fourth, `{"ok":true}`, 0)
		res = s.reconcile()
		Expect(s.get().Status.Ready).To(BeTrue())
		Expect(res.RequeueAfter).To(BeNumerically(">", StoreRetryInterval))
		Eventually(func() []batchv1.Job { return s.jobs() }).Should(BeEmpty(), "the failed Jobs and the passing one are gone")

		By("not probing again at once now that the interval changed")
		res = s.reconcile()
		Expect(s.get().Status.ProbeJob).To(BeEmpty())
		Expect(res.RequeueAfter).To(BeNumerically(">", StoreRetryInterval))
		s.clock += StoreRetryInterval
		s.reconcile()
		Expect(s.get().Status.ProbeJob).To(BeEmpty(), "a reachable store waits for StoreProbeInterval")
	})

	It("falls back to the Job's own failure when no pod reported", func() {
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		s.finishJob(jobName, false)

		s.reconcile()
		cond := s.ready()
		Expect(cond.Reason).To(Equal(ReasonStoreUnreachable))
		Expect(cond.Message).To(ContainSubstring("BackoffLimitExceeded: Job has reached the specified backoff limit"))
		Expect(cond.Message).To(ContainSubstring("logs job/" + jobName))
		Expect(s.events()).To(HaveLen(1))
	})

	It("says the Job failed without reporting why when nothing tells why", func() {
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		job := s.job(jobName)
		now := metav1.Now()
		job.Status.StartTime = &now
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		s.reconcile()
		cond := s.ready()
		Expect(cond.Reason).To(Equal(ReasonStoreUnreachable))
		Expect(cond.Message).To(HavePrefix("probe the Job failed without reporting why; inspect with: kubectl -n " + ns + " logs job/" + jobName))
	})

	It("reads a container that exited without a result", func() {
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		job := s.job(jobName)
		now := metav1.Now()
		job.Status.StartTime = &now
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
		s.podReporting(jobName, "", 137)

		s.reconcile()
		Expect(s.ready().Message).To(ContainSubstring("Completed (exit code 137)"))
	})

	DescribeTable("refuses to probe without the store's Secrets",
		func(build func(s *storeHarness) *odoov1.OdooBackupStore, wantMessage string) {
			store := build(s)
			t.create(store)
			res := s.reconcile()
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			got := s.get()
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.ProbeJob).To(BeEmpty())
			cond := meta.FindStatusCondition(got.Status.Conditions, odoov1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(ReasonStoreSecretBroken))
			Expect(cond.Message).To(ContainSubstring(strings.ReplaceAll(wantMessage, "NAME", s.name)))
			Expect(s.jobs()).To(BeEmpty())
		},
		Entry("no encryption key Secret", func(s *storeHarness) *odoov1.OdooBackupStore {
			return s.pvcStore()
		}, `secret "NAME-key" of OdooBackupStore NAME does not exist`),
		Entry("an encryption key Secret without the key", func(s *storeHarness) *odoov1.OdooBackupStore {
			sec := s.keySecret()
			sec.Data = map[string][]byte{"other": []byte("x")}
			s.t.create(sec)
			return s.pvcStore()
		}, `secret "NAME-key" of OdooBackupStore NAME has no key "password"`),
		Entry("S3 credentials without AWS_SECRET_ACCESS_KEY", func(s *storeHarness) *odoov1.OdooBackupStore {
			s.t.create(s.keySecret())
			s.t.create(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: s.name + "-s3", Namespace: ns},
				Data:       map[string][]byte{odoov1.S3AccessKeyIDKey: []byte("id")},
			})
			store := s.pvcStore()
			store.Spec.PVC = nil
			store.Spec.S3 = &odoov1.S3Store{Bucket: "backups", CredentialsSecret: corev1.LocalObjectReference{Name: s.name + "-s3"}}
			return store
		}, `secret "NAME-s3" of OdooBackupStore NAME has no key "AWS_SECRET_ACCESS_KEY"`),
		Entry("an SSH Secret without known_hosts", func(s *storeHarness) *odoov1.OdooBackupStore {
			s.t.create(s.keySecret())
			s.t.create(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: s.name + "-ssh", Namespace: ns},
				Data:       map[string][]byte{odoov1.SSHPrivateKeyKey: []byte("key")},
			})
			store := s.pvcStore()
			store.Spec.PVC = nil
			store.Spec.SFTP = &odoov1.SFTPStore{Host: "backup.example.com", User: "odoo", SSHSecret: corev1.LocalObjectReference{Name: s.name + "-ssh"}}
			return store
		}, `secret "NAME-ssh" of OdooBackupStore NAME has no key "known_hosts"`),
		Entry("an SSH Secret without a private key", func(s *storeHarness) *odoov1.OdooBackupStore {
			s.t.create(s.keySecret())
			s.t.create(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: s.name + "-ssh", Namespace: ns},
				Data:       map[string][]byte{odoov1.SSHKnownHostsKey: []byte("hosts")},
			})
			store := s.pvcStore()
			store.Spec.PVC = nil
			store.Spec.SFTP = &odoov1.SFTPStore{Host: "backup.example.com", User: "odoo", SSHSecret: corev1.LocalObjectReference{Name: s.name + "-ssh"}}
			return store
		}, `secret "NAME-ssh" of OdooBackupStore NAME has no key "id_ed25519" or "ssh-privatekey"`),
	)

	It("probes with the private key of a kubernetes.io/ssh-auth Secret", func() {
		t.create(s.keySecret())
		t.create(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: s.name + "-ssh", Namespace: ns},
			Type:       corev1.SecretTypeSSHAuth,
			Data: map[string][]byte{
				corev1.SSHAuthPrivateKey: []byte("key"),
				odoov1.SSHKnownHostsKey:  []byte("hosts"),
			},
		})
		store := s.pvcStore()
		store.Spec.PVC = nil
		store.Spec.SFTP = &odoov1.SFTPStore{Host: "backup.example.com", User: "odoo", SSHSecret: corev1.LocalObjectReference{Name: s.name + "-ssh"}}
		t.create(store)
		s.reconcile()
		Expect(s.ready().Reason).NotTo(Equal(ReasonStoreSecretBroken))
		Expect(s.get().Status.ProbeJob).NotTo(BeEmpty())
		Expect(s.jobs()).To(HaveLen(1))
	})

	It("probes once the missing Secret appears", func() {
		t.create(s.pvcStore())
		Expect(s.reconcile().RequeueAfter).To(Equal(time.Minute))
		Expect(s.ready().Reason).To(Equal(ReasonStoreSecretBroken))

		t.create(s.keySecret())
		s.reconcile()
		Expect(s.get().Status.ProbeJob).NotTo(BeEmpty())
		Expect(s.jobs()).To(HaveLen(1))
	})

	It("probes again when a Secret or the spec changes", func() {
		s.createPVCStore()
		s.reconcile()
		first := s.get().Status
		s.finishJob(first.ProbeJob, true)
		s.podReporting(first.ProbeJob, `{"ok":true}`, 0)
		s.reconcile()
		Expect(s.get().Status.Ready).To(BeTrue())

		By("changing the encryption key Secret")
		sec := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: s.name + "-key"}, sec)).To(Succeed())
		sec.Data["password"] = []byte("rotated")
		Expect(k8sClient.Update(ctx, sec)).To(Succeed())
		s.reconcile()
		second := s.get().Status
		Expect(second.ProbedHash).NotTo(Equal(first.ProbedHash))
		Expect(second.ProbeJob).NotTo(BeEmpty())
		Expect(second.ProbeJob).NotTo(Equal(first.ProbeJob))
		Expect(s.job(second.ProbeJob)).NotTo(BeNil())
		s.finishJob(second.ProbeJob, true)
		s.podReporting(second.ProbeJob, `{"ok":true}`, 0)
		s.reconcile()

		By("changing the spec")
		store := s.get()
		Expect(store.Status.ObservedGeneration).To(Equal(int64(1)))
		store.Spec.PVC.SubPath = "odoo"
		Expect(k8sClient.Update(ctx, store)).To(Succeed())
		s.reconcile()
		store = s.get()
		Expect(store.Generation).To(Equal(int64(2)))
		Expect(store.Status.ObservedGeneration).To(Equal(int64(2)))
		Expect(store.Status.ProbedHash).NotTo(Equal(second.ProbedHash))
		Expect(store.Status.ProbeJob).NotTo(BeEmpty())
		Expect(store.Status.ProbeJob).NotTo(Equal(second.ProbeJob))
		Expect(s.job(store.Status.ProbeJob)).NotTo(BeNil())
	})

	It("waits for the backup image without creating a Job", func() {
		s.imageErr = errors.New("the backup image is not known yet")
		s.createPVCStore()
		res := s.reconcile()
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		store := s.get()
		Expect(store.Status.ProbeJob).To(BeEmpty())
		Expect(store.Status.Ready).To(BeFalse())
		cond := meta.FindStatusCondition(store.Status.Conditions, odoov1.ConditionReady)
		Expect(cond.Reason).To(Equal(ReasonBackupImageUnknown))
		Expect(cond.Message).To(Equal("the backup image is not known yet"))
		Expect(s.jobs()).To(BeEmpty())

		s.imageErr = nil
		s.reconcile()
		Expect(s.jobs()).To(HaveLen(1))
	})

	It("does not probe again when the cache still shows the finished probe", func() {
		s.createPVCStore()
		s.reconcile()
		stale := s.get()
		jobName := stale.Status.ProbeJob
		s.finishJob(jobName, true)
		s.podReporting(jobName, `{"ok":true}`, 0)
		s.reconcile()
		Expect(s.get().Status.Ready).To(BeTrue())
		Eventually(func() *batchv1.Job { return s.job(jobName) }).Should(BeNil())

		By("reconciling while the cache still holds the store as it was before the probe finished")
		probed := s.get().Status.ProbedHash
		s.r.Client = staleStoreClient{Client: k8sClient, store: stale}
		s.reconcile()
		s.r.Client = k8sClient
		s.reconcile()
		Expect(s.jobs()).To(BeEmpty())
		store := s.get()
		Expect(store.Status.Ready).To(BeTrue())
		Expect(store.Status.ProbeJob).To(BeEmpty())
		Expect(store.Status.ProbedHash).To(Equal(probed))
	})

	It("restarts the probe when its Job vanishes", func() {
		s.createPVCStore()
		s.reconcile()
		jobName := s.get().Status.ProbeJob
		propagation := metav1.DeletePropagationBackground
		Expect(k8sClient.Delete(ctx, s.job(jobName), &client.DeleteOptions{PropagationPolicy: &propagation})).To(Succeed())
		Eventually(func() *batchv1.Job { return s.job(jobName) }).Should(BeNil())

		res := s.reconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		store := s.get()
		Expect(store.Status.ProbeJob).To(BeEmpty())
		Expect(store.Status.ProbedHash).To(BeEmpty())

		s.reconcile()
		store = s.get()
		Expect(store.Status.ProbeJob).To(Equal(jobName), "the same spec and Secrets name the same Job")
		Expect(s.job(jobName)).NotTo(BeNil())
	})

	It("does not probe a store being deleted", func() {
		s.createPVCStore()
		store := s.get()
		controllerutil.AddFinalizer(store, "test.odoo.abugharbia.com/hold")
		Expect(k8sClient.Update(ctx, store)).To(Succeed())
		Expect(k8sClient.Delete(ctx, store)).To(Succeed())
		res := s.reconcile()
		Expect(res).To(Equal(ctrl.Result{}))
		Expect(s.jobs()).To(BeEmpty(), "a store being deleted is not probed")
	})

	It("maps a Secret to the stores that read it", func() {
		other := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bwName("bs-other")}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, other) })

		base := bwName("map")
		key := func(name string) *corev1.SecretKeySelector {
			return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "password"}
		}
		s3 := &odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: base + "-s3", Namespace: ns},
			Spec: odoov1.OdooBackupStoreSpec{
				S3:                  &odoov1.S3Store{Bucket: "backups", CredentialsSecret: corev1.LocalObjectReference{Name: base + "-creds"}},
				EncryptionKeySecret: key(base + "-key"),
			},
		}
		sftp := &odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: base + "-sftp", Namespace: ns},
			Spec: odoov1.OdooBackupStoreSpec{
				SFTP:                &odoov1.SFTPStore{Host: "h", User: "u", SSHSecret: corev1.LocalObjectReference{Name: base + "-ssh"}},
				EncryptionKeySecret: key(base + "-key"),
			},
		}
		pvc := &odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: base + "-pvc", Namespace: ns},
			Spec: odoov1.OdooBackupStoreSpec{
				PVC:                 &odoov1.PVCStore{ClaimName: "c"},
				EncryptionKeySecret: key(base + "-key2"),
			},
		}
		elsewhere := &odoov1.OdooBackupStore{
			ObjectMeta: metav1.ObjectMeta{Name: base + "-pvc", Namespace: other.Name},
			Spec: odoov1.OdooBackupStoreSpec{
				PVC:                 &odoov1.PVCStore{ClaimName: "c"},
				EncryptionKeySecret: key(base + "-key"),
			},
		}
		for _, st := range []*odoov1.OdooBackupStore{s3, sftp, pvc, elsewhere} {
			t.create(st)
		}
		mapped := func(namespace, name string) []reconcile.Request {
			return s.r.storesUsingSecret(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}})
		}
		req := func(st *odoov1.OdooBackupStore) reconcile.Request {
			return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(st)}
		}
		Expect(mapped(ns, base+"-key")).To(ConsistOf(req(s3), req(sftp)))
		Expect(mapped(ns, base+"-creds")).To(ConsistOf(req(s3)))
		Expect(mapped(ns, base+"-ssh")).To(ConsistOf(req(sftp)))
		Expect(mapped(ns, base+"-key2")).To(ConsistOf(req(pvc)))
		Expect(mapped(ns, base+"-unrelated")).To(BeEmpty())
		Expect(mapped(other.Name, base+"-key")).To(ConsistOf(req(elsewhere)))
		Expect(mapped(other.Name, base+"-creds")).To(BeEmpty())
	})
})

// ---- OdooBackup and OdooRestore ---------------------------------------------

func newBackup(name, instance string) *odoov1.OdooBackup {
	return &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       odoov1.OdooBackupSpec{OdooDeployment: instance},
	}
}

func newRestore(name, instance string) *odoov1.OdooRestore {
	return &odoov1.OdooRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       odoov1.OdooRestoreSpec{OdooDeployment: instance, Backup: "some-backup"},
	}
}

func getBackup(name string) *odoov1.OdooBackup {
	b := &odoov1.OdooBackup{}
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, b)).To(Succeed())
	return b
}

func getRestore(name string) *odoov1.OdooRestore {
	rs := &odoov1.OdooRestore{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, rs)
	if apierrors.IsNotFound(err) {
		return nil
	}
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return rs
}

func setBackupPhase(name, phase string) {
	b := getBackup(name)
	b.Status.Phase = phase
	b.Status.Message = "set by the test"
	b.Status.Job = "some-job"
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, b)).To(Succeed())
}

func setRestorePhase(name, phase string) {
	rs := getRestore(name)
	rs.Status.Phase = phase
	rs.Status.Message = "set by the test"
	rs.Status.Job = "some-job"
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, rs)).To(Succeed())
}

var _ = Describe("OdooBackup and OdooRestore reconcilers", func() {
	var t *bwTracker
	var h *harness
	var br *OdooBackupReconciler
	var rr *OdooRestoreReconciler

	reconcileBackup := func(name string) *odoov1.OdooBackup {
		_, err := br.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return getBackup(name)
	}
	reconcileRestore := func(name string) *odoov1.OdooRestore {
		_, err := rr.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return getRestore(name)
	}

	BeforeEach(func() {
		t = &bwTracker{}
		h = newHarness()
		br = &OdooBackupReconciler{Client: k8sClient}
		rr = &OdooRestoreReconciler{Client: k8sClient}
	})
	AfterEach(func() {
		t.cleanup()
		h.cleanup()
	})

	Context("OdooBackup", func() {
		It("queues a new backup on an existing OdooDeployment", func() {
			h.create()
			name := bwName("bk")
			t.create(newBackup(name, h.name))
			b := reconcileBackup(name)
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhasePending))
			Expect(b.Status.Message).To(Equal("queued on OdooDeployment " + h.name))

			By("leaving a pending backup alone")
			rv := b.ResourceVersion
			Expect(reconcileBackup(name).ResourceVersion).To(Equal(rv))
		})

		It("waits for a missing OdooDeployment, then queues when it appears", func() {
			name := bwName("bk")
			t.create(newBackup(name, h.name))
			b := reconcileBackup(name)
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhasePending))
			Expect(b.Status.Message).To(Equal(fmt.Sprintf("waiting for OdooDeployment %s, which does not exist", h.name)))

			h.create()
			b = reconcileBackup(name)
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhasePending))
			Expect(b.Status.Message).To(ContainSubstring("waiting"), "the backup keeps its message until the OdooDeployment reconciler takes it")
		})

		DescribeTable("fails a running backup whose OdooDeployment is gone",
			func(phase string) {
				name := bwName("bk")
				t.create(newBackup(name, h.name))
				setBackupPhase(name, phase)
				b := reconcileBackup(name)
				Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
				Expect(b.Status.Message).To(Equal(fmt.Sprintf("OdooDeployment %s was deleted while the backup was running", h.name)))
				Expect(b.Status.Job).To(BeEmpty())
				Expect(b.Status.RetryAfter).To(BeNil())
			},
			Entry("Running", odoov1.BackupPhaseRunning),
			Entry("WaitingForFilestoreCleanup", odoov1.BackupPhaseWaitingForFilestoreCleanup),
		)

		It("treats an OdooDeployment being deleted as gone", func() {
			od := h.create(func(od *odoov1.OdooDeployment) {
				od.Finalizers = []string{"test.odoo.abugharbia.com/hold"}
			})
			Expect(k8sClient.Delete(ctx, od)).To(Succeed())
			DeferCleanup(func() {
				if cur := h.get(); cur != nil {
					cur.Finalizers = nil
					_ = k8sClient.Update(ctx, cur)
				}
			})
			name := bwName("bk")
			t.create(newBackup(name, h.name))
			setBackupPhase(name, odoov1.BackupPhaseRunning)
			Expect(reconcileBackup(name).Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
		})

		DescribeTable("leaves other backups untouched",
			func(phase string, instanceExists bool) {
				if instanceExists {
					h.create()
				}
				name := bwName("bk")
				t.create(newBackup(name, h.name))
				setBackupPhase(name, phase)
				before := getBackup(name)
				after := reconcileBackup(name)
				Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
				Expect(after.Status).To(Equal(before.Status))
			},
			Entry("Completed, instance gone", odoov1.BackupPhaseCompleted, false),
			Entry("CompletedWithWarnings, instance gone", odoov1.BackupPhaseCompletedWithWarnings, false),
			Entry("Failed, instance gone", odoov1.BackupPhaseFailed, false),
			Entry("Running, instance present", odoov1.BackupPhaseRunning, true),
			Entry("Completed, instance present", odoov1.BackupPhaseCompleted, true),
		)

		It("ignores a backup that does not exist", func() {
			_, err := br.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: bwName("gone")}})
			Expect(err).NotTo(HaveOccurred())
		})

		It("maps an OdooDeployment to its unfinished backups", func() {
			od := h.spec()
			names := map[string]string{}
			for _, phase := range []string{"", odoov1.BackupPhasePending, odoov1.BackupPhaseRunning, odoov1.BackupPhaseWaitingForFilestoreCleanup,
				odoov1.BackupPhaseCompleted, odoov1.BackupPhaseCompletedWithWarnings, odoov1.BackupPhaseFailed} {
				name := bwName("bk")
				t.create(newBackup(name, h.name))
				if phase != "" {
					setBackupPhase(name, phase)
				}
				names[phase] = name
			}
			otherName := bwName("bk")
			t.create(newBackup(otherName, h.name+"-other"))

			reqs := br.backupsOf(ctx, od)
			req := func(name string) reconcile.Request {
				return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
			}
			Expect(reqs).To(ConsistOf(
				req(names[""]), req(names[odoov1.BackupPhasePending]),
				req(names[odoov1.BackupPhaseRunning]), req(names[odoov1.BackupPhaseWaitingForFilestoreCleanup]),
			))
		})
	})

	Context("OdooRestore", func() {
		It("queues a new restore on an existing OdooDeployment", func() {
			h.create()
			name := bwName("rs")
			t.create(newRestore(name, h.name))
			rs := reconcileRestore(name)
			Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhasePending))
			Expect(rs.Status.Message).To(Equal("queued on OdooDeployment " + h.name))
		})

		It("waits for a missing OdooDeployment", func() {
			name := bwName("rs")
			t.create(newRestore(name, h.name))
			rs := reconcileRestore(name)
			Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhasePending))
			Expect(rs.Status.Message).To(Equal(fmt.Sprintf("waiting for OdooDeployment %s, which does not exist", h.name)))
		})

		DescribeTable("fails a running restore whose OdooDeployment is gone",
			func(phase string) {
				name := bwName("rs")
				t.create(newRestore(name, h.name))
				setRestorePhase(name, phase)
				rs := reconcileRestore(name)
				Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
				Expect(rs.Status.Message).To(Equal(fmt.Sprintf("OdooDeployment %s was deleted while the restore was running", h.name)))
				Expect(rs.Status.Job).To(BeEmpty())
			},
			Entry("Stopping", odoov1.RestorePhaseStopping),
			Entry("Restoring", odoov1.RestorePhaseRestoring),
		)

		DescribeTable("leaves other restores untouched",
			func(phase string, instanceExists bool) {
				if instanceExists {
					h.create()
				}
				name := bwName("rs")
				t.create(newRestore(name, h.name))
				setRestorePhase(name, phase)
				before := getRestore(name)
				after := reconcileRestore(name)
				Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
				Expect(after.Status).To(Equal(before.Status))
			},
			Entry("Completed, instance gone", odoov1.RestorePhaseCompleted, false),
			Entry("Failed, instance gone", odoov1.RestorePhaseFailed, false),
			Entry("Restoring, instance present", odoov1.RestorePhaseRestoring, true),
			Entry("Pending, instance present", odoov1.RestorePhasePending, true),
		)

		It("releases the finalizer of a restore being deleted whose OdooDeployment is gone", func() {
			name := bwName("rs")
			rs := newRestore(name, h.name)
			rs.Finalizers = []string{odoov1.RestoreFinalizer}
			t.create(rs)
			setRestorePhase(name, odoov1.RestorePhaseRestoring)
			Expect(k8sClient.Delete(ctx, getRestore(name))).To(Succeed())
			Expect(getRestore(name).DeletionTimestamp).NotTo(BeNil())

			Expect(reconcileRestore(name)).To(BeNil(), "the restore is gone once its finalizer is released")
		})

		It("keeps the finalizer of a restore being deleted while its OdooDeployment exists", func() {
			h.create()
			name := bwName("rs")
			rs := newRestore(name, h.name)
			rs.Finalizers = []string{odoov1.RestoreFinalizer}
			t.create(rs)
			setRestorePhase(name, odoov1.RestorePhaseRestoring)
			Expect(k8sClient.Delete(ctx, getRestore(name))).To(Succeed())

			got := reconcileRestore(name)
			Expect(got).NotTo(BeNil())
			Expect(got.Finalizers).To(ContainElement(odoov1.RestoreFinalizer))
			Expect(got.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring), "the OdooDeployment reconciler finishes it")
		})

		It("releases the finalizer of a finished restore whose OdooDeployment is gone", func() {
			name := bwName("rs")
			rs := newRestore(name, h.name)
			rs.Finalizers = []string{odoov1.RestoreFinalizer}
			t.create(rs)
			setRestorePhase(name, odoov1.RestorePhaseCompleted)

			got := reconcileRestore(name)
			Expect(got).NotTo(BeNil())
			Expect(got.Finalizers).NotTo(ContainElement(odoov1.RestoreFinalizer))
			Expect(got.Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
		})

		It("keeps the finalizer of an unfinished restore whose OdooDeployment is gone, failing it", func() {
			name := bwName("rs")
			rs := newRestore(name, h.name)
			rs.Finalizers = []string{odoov1.RestoreFinalizer}
			t.create(rs)
			setRestorePhase(name, odoov1.RestorePhaseRestoring)

			got := reconcileRestore(name)
			Expect(got.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
			Expect(got.Finalizers).To(ContainElement(odoov1.RestoreFinalizer))
			got = reconcileRestore(name)
			Expect(got.Finalizers).NotTo(ContainElement(odoov1.RestoreFinalizer), "released once it is Failed")
		})

		It("maps an OdooDeployment to all of its restores", func() {
			od := h.spec()
			mine := make([]reconcile.Request, 0, 4)
			for _, phase := range []string{"", odoov1.RestorePhaseRestoring, odoov1.RestorePhaseCompleted, odoov1.RestorePhaseFailed} {
				name := bwName("rs")
				t.create(newRestore(name, h.name))
				if phase != "" {
					setRestorePhase(name, phase)
				}
				mine = append(mine, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
			}
			t.create(newRestore(bwName("rs"), h.name+"-other"))
			Expect(rr.restoresOf(ctx, od)).To(ConsistOf(mine))
		})
	})
})

// ---- admission -----------------------------------------------------------------

func expectInvalid(err error, want string) {
	ExpectWithOffset(1, err).To(HaveOccurred())
	ExpectWithOffset(1, apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
	ExpectWithOffset(1, err.Error()).To(ContainSubstring(want))
}

var _ = Describe("Backup CRD admission", func() {
	var t *bwTracker
	BeforeEach(func() { t = &bwTracker{} })
	AfterEach(func() { t.cleanup() })

	Context("OdooRestore", func() {
		It("requires exactly one of backup, snapshotID, from and undo", func() {
			rs := newRestore(bwName("rs"), "od")
			rs.Spec.Backup = ""
			expectInvalid(k8sClient.Create(ctx, rs), "set exactly one of backup, snapshotID, from, undo")

			rs = newRestore(bwName("rs"), "od")
			rs.Spec.SnapshotID = "0123abcd"
			expectInvalid(k8sClient.Create(ctx, rs), "set exactly one of backup, snapshotID, from, undo")

			for _, mutate := range []func(*odoov1.OdooRestore){
				func(rs *odoov1.OdooRestore) {},
				func(rs *odoov1.OdooRestore) { rs.Spec.Backup, rs.Spec.SnapshotID = "", "0123abcd" },
				func(rs *odoov1.OdooRestore) { rs.Spec.Backup, rs.Spec.Undo = "", "earlier-restore" },
				func(rs *odoov1.OdooRestore) {
					rs.Spec.Backup, rs.Spec.From = "", &odoov1.BackupSource{Store: "old", Path: "exports/shop_*.zip"}
				},
			} {
				rs := newRestore(bwName("rs"), "od")
				mutate(rs)
				t.create(rs)
			}
		})

		It("checks spec.from and neutralize", func() {
			from := func(src odoov1.BackupSource, neutralize bool) *odoov1.OdooRestore {
				rs := newRestore(bwName("rs"), "od")
				rs.Spec.Backup, rs.Spec.From, rs.Spec.Neutralize = "", &src, neutralize
				return rs
			}
			expectInvalid(k8sClient.Create(ctx, from(odoov1.BackupSource{Store: "s", Path: "a.zip", Snapshot: "latest"}, false)),
				"snapshot, host and tags are for restic repositories")
			expectInvalid(k8sClient.Create(ctx, from(odoov1.BackupSource{Store: "s", Path: "a.dump", Tags: []string{"x"}}, false)),
				"snapshot, host and tags are for restic repositories")
			for _, bad := range []string{"../a.zip", "a/../b.zip", "/abs.zip", "a//b.zip", "dir/", "a*/b.zip"} {
				expectInvalid(k8sClient.Create(ctx, from(odoov1.BackupSource{Store: "s", Path: bad}, false)), "spec.from.path")
			}
			expectInvalid(k8sClient.Create(ctx, from(odoov1.BackupSource{Store: "s", Path: "shop/odoo-*"}, false)),
				"only a .zip or .dump path may be a pattern")
			expectInvalid(k8sClient.Create(ctx, from(odoov1.BackupSource{Store: "s", Path: "shop/odoo", Snapshot: "LATEST"}, false)),
				"spec.from.snapshot")
			undo := newRestore(bwName("rs"), "od")
			undo.Spec.Backup, undo.Spec.Undo, undo.Spec.Neutralize = "", "r1", true
			expectInvalid(k8sClient.Create(ctx, undo), "neutralize cannot be set")

			for _, src := range []odoov1.BackupSource{
				{Store: "s", Path: "exports/shop_2026-*.zip"},
				{Store: "s", Path: "shop.dump"},
				{Store: "s", Path: "other-ns/odoo", Snapshot: "0123abcd", Host: "other-ns/odoo", Tags: []string{"kind:manual"}},
			} {
				rs := from(src, true)
				t.create(rs)
				cur := getRestore(rs.Name)
				cur.Spec.Neutralize = false
				expectInvalid(k8sClient.Update(ctx, cur), "immutable")
				cur = getRestore(rs.Name)
				cur.Spec.From.Path = "elsewhere.zip"
				cur.Spec.From.Snapshot, cur.Spec.From.Host, cur.Spec.From.Tags = "", "", nil
				expectInvalid(k8sClient.Update(ctx, cur), "immutable")
			}
		})

		It("rejects a malformed snapshot ID", func() {
			for _, id := range []string{"0123abc", "0123ABCD", "../../etc", strings.Repeat("a", 65)} {
				rs := newRestore(bwName("rs"), "od")
				rs.Spec.Backup, rs.Spec.SnapshotID = "", id
				expectInvalid(k8sClient.Create(ctx, rs), "spec.snapshotID")
			}
		})

		It("keeps what it restores and its OdooDeployment immutable", func() {
			rs := newRestore(bwName("rs"), "od")
			t.create(rs)

			update := func(mutate func(*odoov1.OdooRestore)) error {
				cur := getRestore(rs.Name)
				mutate(cur)
				return k8sClient.Update(ctx, cur)
			}
			expectInvalid(update(func(r *odoov1.OdooRestore) { r.Spec.Backup = "another" }), "backup, snapshotID, from, undo and neutralize are immutable")
			expectInvalid(update(func(r *odoov1.OdooRestore) { r.Spec.Backup, r.Spec.SnapshotID = "", "0123abcd" }), "backup, snapshotID, from, undo and neutralize are immutable")
			expectInvalid(update(func(r *odoov1.OdooRestore) { r.Spec.Backup, r.Spec.Undo = "", "x" }), "backup, snapshotID, from, undo and neutralize are immutable")
			expectInvalid(update(func(r *odoov1.OdooRestore) { r.Spec.OdooDeployment = "other" }), "spec.odooDeployment is immutable")
			Expect(update(func(r *odoov1.OdooRestore) {
				r.Spec.KeepPrevious = &metav1.Duration{Duration: time.Hour}
			})).To(Succeed(), "keepPrevious may change")

			snap := newRestore(bwName("rs"), "od")
			snap.Spec.Backup, snap.Spec.SnapshotID = "", "0123abcd"
			t.create(snap)
			cur := getRestore(snap.Name)
			cur.Spec.SnapshotID = "0123abce"
			expectInvalid(k8sClient.Update(ctx, cur), "backup, snapshotID, from, undo and neutralize are immutable")

			undo := newRestore(bwName("rs"), "od")
			undo.Spec.Backup, undo.Spec.Undo = "", "r1"
			t.create(undo)
			cur = getRestore(undo.Name)
			cur.Spec.Undo = "r2"
			expectInvalid(k8sClient.Update(ctx, cur), "backup, snapshotID, from, undo and neutralize are immutable")
		})
	})

	Context("OdooBackup", func() {
		It("keeps spec.odooDeployment immutable and keep mutable", func() {
			b := newBackup(bwName("bk"), "od")
			t.create(b)
			cur := getBackup(b.Name)
			cur.Spec.OdooDeployment = "other"
			expectInvalid(k8sClient.Update(ctx, cur), "spec.odooDeployment is immutable")

			cur = getBackup(b.Name)
			cur.Spec.Keep = true
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		})

		It("requires an OdooDeployment", func() {
			expectInvalid(k8sClient.Create(ctx, newBackup(bwName("bk"), "")), "spec.odooDeployment")
		})
	})

	Context("OdooBackupStore", func() {
		store := func(mutate func(*odoov1.OdooBackupStoreSpec)) *odoov1.OdooBackupStore {
			st := &odoov1.OdooBackupStore{
				ObjectMeta: metav1.ObjectMeta{Name: bwName("bs"), Namespace: ns},
				Spec: odoov1.OdooBackupStoreSpec{
					EncryptionKeySecret: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "k"}, Key: "password"},
				},
			}
			mutate(&st.Spec)
			return st
		}
		s3 := &odoov1.S3Store{Bucket: "backups", CredentialsSecret: corev1.LocalObjectReference{Name: "c"}}
		sftp := &odoov1.SFTPStore{Host: "h", User: "u", SSHSecret: corev1.LocalObjectReference{Name: "s"}}
		nfs := &odoov1.NFSStore{Server: "nfs", Path: "/exports"}
		pvc := &odoov1.PVCStore{ClaimName: "c"}

		It("accepts each kind of store and applies defaults", func() {
			for _, mutate := range []func(*odoov1.OdooBackupStoreSpec){
				func(s *odoov1.OdooBackupStoreSpec) { s.S3 = s3 },
				func(s *odoov1.OdooBackupStoreSpec) { s.SFTP = sftp.DeepCopy() },
				func(s *odoov1.OdooBackupStoreSpec) { s.NFS = nfs },
				func(s *odoov1.OdooBackupStoreSpec) { s.PVC = pvc },
			} {
				st := store(mutate)
				t.create(st)
				Expect(st.Spec.Format).To(Equal(odoov1.BackupStoreFormatRestic))
				if st.Spec.SFTP != nil {
					Expect(st.Spec.SFTP.Port).To(Equal(int32(22)))
				}
			}
		})

		It("needs a key unless it is readOnly, and never checks or prunes a readOnly store", func() {
			noKey := func(s *odoov1.OdooBackupStoreSpec) { s.PVC, s.EncryptionKeySecret = pvc, nil }
			expectInvalid(k8sClient.Create(ctx, store(noKey)), "encryptionKeySecret is required unless the store is readOnly")
			t.create(store(func(s *odoov1.OdooBackupStoreSpec) { noKey(s); s.ReadOnly = true }))
			t.create(store(func(s *odoov1.OdooBackupStoreSpec) { s.PVC, s.ReadOnly = pvc, true }))
			expectInvalid(k8sClient.Create(ctx, store(func(s *odoov1.OdooBackupStoreSpec) {
				s.PVC, s.ReadOnly, s.Check.Schedule = pvc, true, "@weekly"
			})), "a readOnly store is never checked or pruned")
			expectInvalid(k8sClient.Create(ctx, store(func(s *odoov1.OdooBackupStoreSpec) {
				s.PVC, s.ReadOnly, s.Prune.Schedule = pvc, true, "@weekly"
			})), "a readOnly store is never checked or pruned")
		})

		DescribeTable("requires exactly one kind of store",
			func(mutate func(*odoov1.OdooBackupStoreSpec)) {
				expectInvalid(k8sClient.Create(ctx, store(mutate)), "set exactly one of s3, sftp, nfs, pvc")
			},
			Entry("none", func(s *odoov1.OdooBackupStoreSpec) {}),
			Entry("s3 and pvc", func(s *odoov1.OdooBackupStoreSpec) { s.S3, s.PVC = s3, pvc }),
			Entry("sftp and nfs", func(s *odoov1.OdooBackupStoreSpec) { s.SFTP, s.NFS = sftp.DeepCopy(), nfs }),
		)

		DescribeTable("rejects paths that leave the store",
			func(mutate func(*odoov1.OdooBackupStoreSpec), field string) {
				expectInvalid(k8sClient.Create(ctx, store(mutate)), field)
			},
			Entry("s3 ..", func(s *odoov1.OdooBackupStoreSpec) {
				c := *s3
				c.Path = "backups/../other"
				s.S3 = &c
			}, "path segments cannot be . or .."),
			Entry("sftp leading ..", func(s *odoov1.OdooBackupStoreSpec) {
				c := sftp.DeepCopy()
				c.Path = "../root"
				s.SFTP = c
			}, "path segments cannot be . or .."),
			Entry("pvc .", func(s *odoov1.OdooBackupStoreSpec) { s.PVC = &odoov1.PVCStore{ClaimName: "c", SubPath: "a/./b"} }, "path segments cannot be . or .."),
			Entry("pvc double slash", func(s *odoov1.OdooBackupStoreSpec) { s.PVC = &odoov1.PVCStore{ClaimName: "c", SubPath: "a//b"} }, "spec.pvc.subPath"),
			Entry("relative nfs export", func(s *odoov1.OdooBackupStoreSpec) { s.NFS = &odoov1.NFSStore{Server: "nfs", Path: "exports"} }, "spec.nfs.path"),
		)

		It("accepts nested and absolute paths", func() {
			c := sftp.DeepCopy()
			c.Path = "/srv/backups/odoo/"
			t.create(store(func(s *odoov1.OdooBackupStoreSpec) { s.SFTP = c }))
			t.create(store(func(s *odoov1.OdooBackupStoreSpec) { s.PVC = &odoov1.PVCStore{ClaimName: "c", SubPath: "a.b/c..d"} }))
		})
	})

	Context("OdooDeployment spec.backup", func() {
		var h *harness
		BeforeEach(func() { h = newHarness() })
		AfterEach(func() { h.cleanup() })

		It("applies the backup defaults", func() {
			h.create()
			od := h.get()
			Expect(od.Spec.Backup.Stores).To(BeEmpty())
			Expect(od.Spec.Backup.Consistency).To(Equal(odoov1.BackupConsistencyOnline))
			Expect(od.Spec.Backup.ActiveDeadlineSeconds).To(HaveValue(Equal(int64(21600))))
			Expect(od.Spec.Backup.Restore.KeepPrevious).NotTo(BeNil())
			Expect(od.Spec.Backup.Restore.KeepPrevious.Duration).To(Equal(72 * time.Hour))
		})

		It("accepts up to four different stores", func() {
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Stores = []string{"a", "b", "a"}
			})), "must not repeat a store")
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Stores = []string{"a", "b", "c", "d", "e"}
			})), "spec.backup.stores")

			h.create(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Stores = []string{"a", "b"}
				od.Spec.Backup.Consistency = odoov1.BackupConsistencyOffline
			})
			od := h.get()
			Expect(od.Spec.Backup.Stores).To(Equal([]string{"a", "b"}))
			Expect(od.Spec.Backup.Consistency).To(Equal(odoov1.BackupConsistencyOffline))
			By("the retention defaults are filled in")
			Expect(od.Spec.Backup.TimeZone).To(Equal("UTC"))
			Expect(od.Spec.Backup.Retention.Counts()).To(Equal(odoov1.RetentionCounts{Daily: 7, Weekly: 4, Monthly: 12, PreUpgrade: 5}))
			Expect(*od.Spec.Backup.Retention.Daily).To(Equal(int32(7)))
		})

		It("needs a store for a backup before upgrades", func() {
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.BeforeUpgrade = odoov1.BeforeUpgradeRequired
			})), "spec.backup.beforeUpgrade needs spec.backup.stores")
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.BeforeUpgrade = "Always"
			})), "beforeUpgrade")
			h.create(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.BeforeUpgrade = odoov1.BeforeUpgradeOff
			})
			Expect(h.get().Spec.Backup.BeforeUpgradeValue()).To(Equal(odoov1.BeforeUpgradeOff))
		})

		It("rejects a keep-within that restic cannot read and a negative count", func() {
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Retention.Within = "2w"
			})), "spec.backup.retention.within")
			neg := int32(-1)
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Retention.Daily = &neg
			})), "spec.backup.retention.daily")
		})

		It("rejects an unknown consistency and a too-short deadline", func() {
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Consistency = "Sometimes"
			})), "spec.backup.consistency")
			short := int64(10)
			expectInvalid(k8sClient.Create(ctx, h.spec(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.ActiveDeadlineSeconds = &short
			})), "spec.backup.activeDeadlineSeconds")
		})
	})
})

// staleStoreClient answers Gets of one OdooBackupStore with an old copy, as a
// lagging informer cache would.
type staleStoreClient struct {
	client.Client
	store *odoov1.OdooBackupStore
}

func (c staleStoreClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if out, ok := obj.(*odoov1.OdooBackupStore); ok && key == client.ObjectKeyFromObject(c.store) {
		c.store.DeepCopyInto(out)
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
