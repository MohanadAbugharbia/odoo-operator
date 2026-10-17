package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// backupHarness is a running instance with a PVC store.
type backupHarness struct {
	*harness
	store string
	objs  []client.Object
}

func newBackupHarness(mutators ...func(*odoov1.OdooDeployment)) *backupHarness {
	h := &backupHarness{harness: newHarness()}
	h.r.BackupImage = "ghcr.io/example/odoo-operator-backup:test"
	h.r.Version = "test"
	h.store = h.name + "-store"
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: h.name + "-restic", Namespace: ns},
		Data:       map[string][]byte{"password": []byte("s3cret")},
	}
	store := &odoov1.OdooBackupStore{
		ObjectMeta: metav1.ObjectMeta{Name: h.store, Namespace: ns},
		Spec: odoov1.OdooBackupStoreSpec{
			PVC: &odoov1.PVCStore{ClaimName: "backups"},
			EncryptionKeySecret: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: key.Name}, Key: "password",
			},
		},
	}
	h.track(key, store)
	h.create(append([]func(*odoov1.OdooDeployment){func(od *odoov1.OdooDeployment) {
		od.Spec.Backup.Stores = []string{h.store}
	}}, mutators...)...)
	h.bootToRunning()
	h.setAvailable(1)
	h.reconcile()
	return h
}

// track creates objects and deletes them after the test.
func (h *backupHarness) track(objs ...client.Object) {
	for _, o := range objs {
		Expect(k8sClient.Create(ctx, o)).To(Succeed())
		h.objs = append(h.objs, o)
	}
}

func (h *backupHarness) cleanupAll() {
	for _, o := range h.objs {
		if rs, ok := o.(*odoov1.OdooRestore); ok {
			fresh := &odoov1.OdooRestore{}
			if k8sClient.Get(ctx, client.ObjectKeyFromObject(rs), fresh) == nil && controllerutil.RemoveFinalizer(fresh, odoov1.RestoreFinalizer) {
				_ = k8sClient.Update(ctx, fresh)
			}
		}
		_ = k8sClient.Delete(ctx, o)
	}
	h.cleanup()
}

func (h *backupHarness) newBackup(name string) *odoov1.OdooBackup {
	b := &odoov1.OdooBackup{
		ObjectMeta: metav1.ObjectMeta{Name: h.name + "-" + name, Namespace: ns},
		Spec:       odoov1.OdooBackupSpec{OdooDeployment: h.name},
	}
	h.track(b)
	return b
}

func (h *backupHarness) newRestore(name string, set func(*odoov1.OdooRestoreSpec)) *odoov1.OdooRestore {
	rs := &odoov1.OdooRestore{
		ObjectMeta: metav1.ObjectMeta{Name: h.name + "-" + name, Namespace: ns},
		Spec:       odoov1.OdooRestoreSpec{OdooDeployment: h.name},
	}
	set(&rs.Spec)
	h.track(rs)
	return rs
}

func (h *backupHarness) backup(name string) *odoov1.OdooBackup {
	b := &odoov1.OdooBackup{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: h.name + "-" + name}, b)).To(Succeed())
	return b
}

func (h *backupHarness) restore(name string) *odoov1.OdooRestore {
	rs := &odoov1.OdooRestore{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: h.name + "-" + name}, rs)
	if apierrors.IsNotFound(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	return rs
}

// completedSnapshot is the snapshot of completedBackup.
const completedSnapshot = "0123456789abcdef"

// completedBackup is backup "one", finished earlier.
func (h *backupHarness) completedBackup() {
	b := h.newBackup("one")
	b.Status = odoov1.OdooBackupStatus{
		Phase: odoov1.BackupPhaseCompleted, Store: h.store, SnapshotID: completedSnapshot,
		Repository: layout.Repository(ns, h.name),
	}
	Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
}

// finishJob ends a backup Job: its pod's container terminates with the
// result as its termination message, then the Job is marked done.
func (h *backupHarness) finishJob(name string, res *result.Result) {
	job := h.job(name)
	Expect(job).NotTo(BeNil(), "job %s should exist", name)
	exit := int32(0)
	if res != nil && !res.OK {
		exit = 1
	}
	if res != nil {
		msg := result.Encode(*res)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-pod", Namespace: ns,
				Labels: map[string]string{"job-name": name, "batch.kubernetes.io/job-name": name},
			},
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: jobspec.ContainerName, Image: "x"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)) })
		pod.Status.Phase = corev1.PodSucceeded
		if exit != 0 {
			pod.Status.Phase = corev1.PodFailed
		}
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: jobspec.ContainerName, Image: "x",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: exit, Message: string(msg), Reason: "Completed", FinishedAt: metav1.Now(),
			}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
	}
	if exit == 0 && res != nil {
		markJobSucceeded(job)
	} else {
		markJobFailed(job)
	}
	Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
}

func (h *backupHarness) replicas() int32 {
	d := h.deployment()
	Expect(d).NotTo(BeNil())
	return *d.Spec.Replicas
}

func jobArgs(job *batchv1.Job) string {
	return strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")
}

var _ = Describe("Backups and restores", func() {
	var h *backupHarness

	AfterEach(func() {
		if h != nil {
			h.cleanupAll()
		}
	})

	Describe("a backup", func() {
		It("runs online next to Odoo and records the snapshot", func() {
			h = newBackupHarness()
			b := h.newBackup("one")

			_, od := h.reconcile()
			op := od.Status.CurrentOperation
			Expect(op.Kind).To(Equal(odoov1.OperationBackup))
			Expect(op.Name).To(Equal(b.Name))
			Expect(op.StoppedOdoo).To(BeFalse())
			Expect(op.Job).NotTo(BeEmpty())
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning), "an online backup does not change the phase")
			Expect(h.replicas()).To(Equal(int32(1)), "Odoo keeps running")

			job := h.job(op.Job)
			Expect(job).NotTo(BeNil())
			Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/example/odoo-operator-backup:test"))
			Expect(jobArgs(job)).To(ContainSubstring("--wait-for-cleanup"))
			Expect(jobArgs(job)).To(ContainSubstring("--backup " + b.Name))
			Expect(metav1.GetControllerOf(job).UID).To(Equal(od.UID))
			Expect(*job.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())

			fresh := h.backup("one")
			Expect(fresh.Status.Phase).To(Equal(odoov1.BackupPhaseRunning))
			Expect(fresh.Status.Job).To(Equal(op.Job))
			Expect(fresh.Status.Attempts).To(Equal(int32(1)))
			Expect(fresh.Status.Store).To(Equal(h.store))
			Expect(fresh.Status.Repository).To(Equal(ns + "/" + h.name))
			Expect(fresh.Status.Consistency).To(Equal("Online"))
			Expect(fresh.Status.StartedAt).NotTo(BeNil())

			By("a second reconcile while it runs creates nothing new")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Job).To(Equal(op.Job))

			By("the Job reports the snapshot")
			h.finishJob(op.Job, &result.Result{
				OK: true, SnapshotID: "0123456789abcdef0123", OdooVersion: "18.0", PgServerVersion: "16.4",
				DatabaseBytes: 2048, FilestoreBytes: 4096, FilestoreFiles: 3, AddedBytes: 100,
			})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			fresh = h.backup("one")
			Expect(fresh.Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
			Expect(fresh.Status.SnapshotID).To(Equal("0123456789abcdef0123"))
			Expect(fresh.Status.OdooVersion).To(Equal("18.0"))
			Expect(fresh.Status.PgServerVersion).To(Equal("16.4"))
			Expect(fresh.Status.Image).To(Equal("odoo:a"))
			Expect(fresh.Status.FilestoreFiles).To(Equal(int64(3)))
			Expect(fresh.Status.CompletedAt).NotTo(BeNil())
			Expect(fresh.Status.Message).To(ContainSubstring("snapshot 01234567"))
			Eventually(func() *batchv1.Job { return h.job(op.Job) }).Should(BeNil(), "the finished job is deleted")
		})

		It("is completed with warnings when files are missing", func() {
			h = newBackupHarness()
			h.newBackup("one")
			_, od := h.reconcile()
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{
				OK: true, SnapshotID: "abcdef0123456789", MissingFiles: 2, MissingNames: []string{"ab/x", "cd/y"},
				Warnings: []string{"2 attachment(s) the database refers to are in neither the filestore nor the previous backup"},
			})
			h.reconcile()
			b := h.backup("one")
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseCompletedWithWarnings))
			Expect(b.Status.MissingFiles).To(Equal(int32(2)))
			Expect(b.Status.MissingFileNames).To(ConsistOf("ab/x", "cd/y"))
		})

		It("waits for Odoo's filestore cleanup and runs again later", func() {
			h = newBackupHarness()
			h.newBackup("one")
			_, od := h.reconcile()
			first := od.Status.CurrentOperation.Job
			h.finishJob(first, &result.Result{OK: true, Deferred: true, Warnings: []string{"Odoo's filestore cleanup is due in 3m0s"}})
			res, od := h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "a waiting backup frees the instance")
			b := h.backup("one")
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseWaitingForFilestoreCleanup))
			Expect(b.Status.Message).To(ContainSubstring("cleanup is due"))
			Expect(b.Status.WaitingSince).NotTo(BeNil())
			Expect(b.Status.RetryAfter).NotTo(BeNil())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
			Expect(res.RequeueAfter).To(BeNumerically("<=", 2*time.Minute))

			By("before its retry time nothing starts")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())

			By("at its retry time it runs again with a new Job")
			past := metav1.NewTime(time.Now().Add(-time.Second))
			b.Status.RetryAfter = &past
			Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Job).NotTo(BeEmpty())
			Expect(od.Status.CurrentOperation.Job).NotTo(Equal(first))
			Expect(h.backup("one").Status.Attempts).To(Equal(int32(2)))
			Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).To(ContainSubstring("--wait-for-cleanup"))

			By("after waiting two hours it no longer waits")
			b = h.backup("one")
			long := metav1.NewTime(time.Now().Add(-3 * time.Hour))
			b.Status.WaitingSince = &long
			Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, Deferred: true, Warnings: []string{"due"}})
			h.reconcile()
			b = h.backup("one")
			b.Status.RetryAfter = &past
			Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
			_, od = h.reconcile()
			Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).NotTo(ContainSubstring("--wait-for-cleanup"))
		})

		It("fails with the step and error the Job reported", func() {
			h = newBackupHarness()
			h.newBackup("one")
			_, od := h.reconcile()
			job := od.Status.CurrentOperation.Job
			h.finishJob(job, &result.Result{OK: false, Step: result.StepUpload, Error: "repository is locked"})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			b := h.backup("one")
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
			Expect(b.Status.Message).To(ContainSubstring("failed at step upload: repository is locked"))
			Expect(b.Status.Message).To(ContainSubstring("kubectl -n " + ns + " logs job/" + job))
			Expect(h.replicas()).To(Equal(int32(1)))
		})

		It("fails without a result when the Job died", func() {
			h = newBackupHarness()
			h.newBackup("one")
			_, od := h.reconcile()
			h.finishJob(od.Status.CurrentOperation.Job, nil)
			h.reconcile()
			b := h.backup("one")
			Expect(b.Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
			Expect(b.Status.Message).NotTo(BeEmpty())
		})

		It("stops Odoo for an offline backup and starts it again", func() {
			h = newBackupHarness(func(od *odoov1.OdooDeployment) {
				od.Spec.Backup.Consistency = odoov1.BackupConsistencyOffline
			})
			h.newBackup("one")
			_, od := h.reconcile()
			Expect(od.Status.CurrentOperation.StoppedOdoo).To(BeTrue())
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseBackingUp))
			Expect(h.replicas()).To(Equal(int32(0)))
			job := h.job(od.Status.CurrentOperation.Job)
			Expect(jobArgs(job)).NotTo(ContainSubstring("--wait-for-cleanup"))
			Expect(jobArgs(job)).To(ContainSubstring("--consistency Offline"))
			Expect(h.backup("one").Status.Consistency).To(Equal("Offline"))

			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, SnapshotID: "abcdef0123456789"})
			_, od = h.reconcile()
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
			Expect(h.replicas()).To(Equal(int32(1)))
		})

		It("fails when the instance has no store or the store is broken", func() {
			h = newBackupHarness()
			od := h.get()
			od.Spec.Backup.Stores = []string{"missing"}
			Expect(k8sClient.Update(ctx, od)).To(Succeed())
			h.newBackup("one")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			Expect(h.backup("one").Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
			Expect(h.backup("one").Status.Message).To(ContainSubstring(`OdooBackupStore "missing" does not exist`))

			od.Spec.Backup.Stores = nil
			Expect(k8sClient.Update(ctx, od)).To(Succeed())
			h.newBackup("two")
			h.reconcile()
			Expect(h.backup("two").Status.Phase).To(Equal(odoov1.BackupPhaseFailed))
			Expect(h.backup("two").Status.Message).To(ContainSubstring("no spec.backup.stores"))
		})

		It("waits for an upgrade and runs backups one at a time, oldest first", func() {
			h = newBackupHarness()
			od := h.get()
			od.Spec.Image = "odoo:b"
			Expect(k8sClient.Update(ctx, od)).To(Succeed())
			h.reconcile()
			_, od = h.passImageCheck()
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseUpgrading))

			h.newBackup("a")
			h.newBackup("b")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "backups wait for the upgrade")
			h.succeedJob(od.Status.CurrentInitJob.Name)
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(h.name+"-a"), "the oldest backup goes first")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-a"))
			Expect(h.backup("b").Status.Phase).NotTo(Equal(odoov1.BackupPhaseRunning))
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, SnapshotID: "aaaaaaaa"})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-b"))
		})

		It("abandons a backup that was deleted, once its Job ended", func() {
			h = newBackupHarness()
			b := h.newBackup("one")
			_, od := h.reconcile()
			job := od.Status.CurrentOperation.Job
			Expect(k8sClient.Delete(ctx, b)).To(Succeed())
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationBackup), "waits for the running Job")
			h.failJob(job)
			h.reconcile()
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		})
	})

	Describe("never two jobs at once", func() {
		// operationJobs are the instance's backup, restore and cleanup Jobs.
		operationJobs := func() []string {
			list := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(ns),
				client.MatchingLabels{odoov1.LabelOdooDeployment: h.name})).To(Succeed())
			var names []string
			for _, j := range list.Items {
				if operationJobKinds[j.Labels[odoov1.LabelJobKind]] && j.DeletionTimestamp.IsZero() {
					names = append(names, j.Name)
				}
			}
			return names
		}
		// forget drops what the instance's status says about its operation,
		// as a lost status write would.
		forget := func(clear func(*odoov1.OperationStatus)) {
			od := h.get()
			clear(&od.Status.CurrentOperation)
			Expect(k8sClient.Status().Update(ctx, od)).To(Succeed())
		}

		It("follows the running Job when the status lost it", func() {
			h = newBackupHarness()
			h.newBackup("one")
			_, od := h.reconcile()
			first := od.Status.CurrentOperation.Job
			Expect(first).NotTo(BeEmpty())

			// The backup's own status still counts the attempt, so a new Job
			// would be attempt 2: what a lagging cache once started.
			forget(func(op *odoov1.OperationStatus) { op.Job = "" })
			Expect(h.backup("one").Status.Attempts).To(Equal(int32(1)))

			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Job).To(Equal(first))
			Expect(operationJobs()).To(ConsistOf(first))

			By("finishing through the followed Job")
			h.finishJob(first, &result.Result{OK: true, SnapshotID: "0123456789abcdef0123", OdooVersion: "18.0"})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			Expect(h.backup("one").Status.Phase).To(Equal(odoov1.BackupPhaseCompleted))
		})

		It("starts no restore while another operation's Job still runs", func() {
			h = newBackupHarness()
			h.completedBackup()
			h.newBackup("two")
			_, od := h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-two"))
			backupJob := od.Status.CurrentOperation.Job
			Expect(backupJob).NotTo(BeEmpty())

			forget(func(op *odoov1.OperationStatus) { *op = odoov1.OperationStatus{} })
			h.newRestore("back", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
			h.reconcile()
			_, od = h.reconcile()
			Expect(operationJobs()).To(ConsistOf(backupJob), "the restore waits")
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
			Expect(od.Status.CurrentOperation.Job).To(BeEmpty())
			ready := condition(od, odoov1.ConditionReady)
			Expect(ready.Reason).To(Equal(ReasonWaitingForOperation))
			Expect(ready.Message).To(ContainSubstring(backupJob))

			By("starting once that Job has ended")
			h.failJob(backupJob)
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Job).NotTo(BeEmpty())
			Expect(operationJobs()).To(ContainElement(od.Status.CurrentOperation.Job))
		})
	})

	Describe("a restore", func() {
		restoreFrom := func(backup string) func(*odoov1.OdooRestoreSpec) {
			return func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-" + backup }
		}

		It("stops Odoo, restores, and starts the backup's image", func() {
			h = newBackupHarness()
			h.completedBackup()
			rs := h.newRestore("r", restoreFrom("one"))

			_, od := h.reconcile()
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseRestoring))
			Expect(h.replicas()).To(Equal(int32(0)))
			op := od.Status.CurrentOperation
			Expect(op.Kind).To(Equal(odoov1.OperationRestore))
			Expect(op.StoppedOdoo).To(BeTrue())
			Expect(op.Job).NotTo(BeEmpty(), "no pods run in envtest, so the Job starts at once")

			fresh := h.restore("r")
			Expect(controllerutil.ContainsFinalizer(fresh, odoov1.RestoreFinalizer)).To(BeTrue())
			Expect(fresh.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
			Expect(layout.ValidSuffix(fresh.Status.Suffix)).To(BeTrue())
			Expect(fresh.Status.SnapshotID).To(Equal(completedSnapshot))
			Expect(fresh.Status.Store).To(Equal(h.store))
			Expect(fresh.Status.Previous.Image).To(Equal("odoo:a"))
			args := jobArgs(h.job(op.Job))
			Expect(args).To(ContainSubstring("--snapshot " + completedSnapshot))
			Expect(args).To(ContainSubstring("--suffix " + fresh.Status.Suffix))
			Expect(args).To(ContainSubstring("--expect-modules base,web"))
			Expect(condition(od, odoov1.ConditionReady).Reason).To(Equal(ReasonRestoreRunning))

			By("a backup created meanwhile waits")
			h.newBackup("later")
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(rs.Name))

			h.finishJob(op.Job, &result.Result{
				OK: true, LiveChanged: true, PreviousKept: true, Image: "odoo:old", UpgradeToken: "t1",
				OdooVersion: "18.0", MissingModules: []string{"web"},
			})
			_, od = h.reconcile()
			fresh = h.restore("r")
			Expect(fresh.Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
			Expect(fresh.Status.Image).To(Equal("odoo:old"))
			Expect(fresh.Status.Previous.State).To(Equal(odoov1.PreviousKept))
			Expect(fresh.Status.Previous.Database).To(Equal(layout.PrevName(h.dbName(), fresh.Status.Suffix)))
			Expect(fresh.Status.Previous.KeptUntil.Time).To(BeTemporally("~", time.Now().Add(72*time.Hour), time.Minute))
			Expect(fresh.Status.Message).To(ContainSubstring("modules web are installed again"))
			Expect(controllerutil.ContainsFinalizer(fresh, odoov1.RestoreFinalizer)).To(BeTrue(), "kept until the cleanup")

			Expect(od.Status.AppliedImage).To(Equal("odoo:old"))
			Expect(od.Status.AppliedUpgradeToken).To(Equal("t1"))
			Expect(od.Status.InitModulesInstalled).To(ConsistOf("base"))
			Expect(od.Status.CurrentOperation.Kind).NotTo(Equal(odoov1.OperationRestore))
		})

		It("leaves the live data alone when it fails before the swap, then cleans up", func() {
			h = newBackupHarness()
			h.completedBackup()
			h.newRestore("r", restoreFrom("one"))
			_, od := h.reconcile()
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: false, Step: result.StepCheck, Error: "table x has 1 rows instead of 2"})
			_, od = h.reconcile()
			fresh := h.restore("r")
			Expect(fresh.Status.Phase).To(Equal(odoov1.RestorePhaseFailed))
			Expect(fresh.Status.Message).To(ContainSubstring("table x has 1 rows instead of 2"))
			Expect(fresh.Status.Message).To(ContainSubstring("the live data was not changed"))
			Expect(od.Status.AppliedImage).To(Equal("odoo:a"))

			By("the cleanup of the half-restored copy runs next, then Odoo is back")
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationCleanup))
			cleanupJob := h.job(od.Status.CurrentOperation.Job)
			Expect(jobArgs(cleanupJob)).To(ContainSubstring("--suffix " + fresh.Status.Suffix))
			h.finishJob(cleanupJob.Name, &result.Result{OK: true})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			fresh = h.restore("r")
			Expect(fresh.Status.CleanedUp).To(BeTrue())
			Expect(controllerutil.ContainsFinalizer(fresh, odoov1.RestoreFinalizer)).To(BeFalse())
			Expect(h.replicas()).To(Equal(int32(1)))
			Expect(od.Status.Phase).To(Equal(odoov1.PhaseRunning))
		})

		for _, tc := range []struct {
			name string
			res  *result.Result
		}{
			{"after the swap started", &result.Result{OK: false, Step: result.StepSwap, Error: "rename failed", LiveChanged: true}},
			{"without a result", nil},
		} {
			It("keeps Odoo stopped and retries when it fails "+tc.name, func() {
				h = newBackupHarness()
				h.completedBackup()
				h.newRestore("r", restoreFrom("one"))
				_, od := h.reconcile()
				first := od.Status.CurrentOperation.Job
				h.finishJob(first, tc.res)
				res, od := h.reconcile()
				fresh := h.restore("r")
				Expect(fresh.Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
				Expect(fresh.Status.LiveChanged).To(BeTrue())
				Expect(fresh.Status.RetryAfter).NotTo(BeNil())
				Expect(fresh.Status.Message).To(ContainSubstring("Odoo stays stopped"))
				Expect(od.Status.Phase).To(Equal(odoov1.PhaseRestoring))
				Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(ReasonRestoreIncomplete))
				Expect(h.replicas()).To(Equal(int32(0)))
				Expect(res.RequeueAfter).To(Equal(30 * time.Second))

				By("nothing else runs meanwhile, and no new Job before the retry time")
				h.newBackup("meanwhile")
				_, od = h.reconcile()
				Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
				Expect(od.Status.CurrentOperation.Job).To(BeEmpty())

				By("the retry runs the same restore again")
				past := metav1.NewTime(time.Now().Add(-time.Second))
				fresh.Status.RetryAfter = &past
				Expect(k8sClient.Status().Update(ctx, fresh)).To(Succeed())
				_, od = h.reconcile()
				second := od.Status.CurrentOperation.Job
				Expect(second).NotTo(BeEmpty())
				Expect(second).NotTo(Equal(first))
				Expect(jobArgs(h.job(second))).To(ContainSubstring("--suffix " + fresh.Status.Suffix))
				Expect(h.restore("r").Status.Attempts).To(Equal(int32(2)))

				By("even a refusal now cannot fail it: it must be finished")
				h.finishJob(second, &result.Result{OK: false, Step: result.StepSwap, Error: "again", LiveChanged: false})
				h.reconcile()
				Expect(h.restore("r").Status.Phase).To(Equal(odoov1.RestorePhaseRestoring))
				Expect(restoreRetryDelay(2)).To(Equal(time.Minute))
			})
		}

		It("retries with a growing delay capped at fifteen minutes", func() {
			Expect(restoreRetryDelay(0)).To(Equal(30 * time.Second))
			Expect(restoreRetryDelay(1)).To(Equal(30 * time.Second))
			Expect(restoreRetryDelay(3)).To(Equal(2 * time.Minute))
			Expect(restoreRetryDelay(10)).To(Equal(15 * time.Minute))
			Expect(restoreRetryDelay(1000)).To(Equal(15 * time.Minute))
		})

		It("replaces a failed upgrade Job, and waits for a running one", func() {
			h = newBackupHarness()
			h.completedBackup()
			od := h.get()
			od.Spec.Image = "odoo:b"
			Expect(k8sClient.Update(ctx, od)).To(Succeed())
			h.reconcile()
			_, od = h.passImageCheck()
			upgrade := od.Status.CurrentInitJob.Name
			Expect(upgrade).NotTo(BeEmpty())

			h.newRestore("r", restoreFrom("one"))
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "a running upgrade is waited for")
			Expect(od.Status.CurrentInitJob.Name).To(Equal(upgrade))

			h.failJob(upgrade)
			_, od = h.reconcile()
			Expect(od.Status.CurrentInitJob.Name).To(BeEmpty())
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationRestore))
			Eventually(func() *batchv1.Job { return h.job(upgrade) }).Should(BeNil())

			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, LiveChanged: true, PreviousKept: true, Image: "odoo:a"})
			_, od = h.reconcile()
			Expect(h.restore("r").Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
			Expect(od.Status.AppliedImage).To(Equal("odoo:a"))
		})

		It("is refused for a backup that cannot be restored", func() {
			h = newBackupHarness()
			other := h.newBackup("pending")
			_ = other
			h.newRestore("missing", restoreFrom("nope"))
			h.newRestore("notdone", restoreFrom("pending"))
			for i := 0; i < 4; i++ {
				h.reconcile()
			}
			for name, want := range map[string]string{
				"missing": `OdooBackup "` + h.name + `-nope" does not exist`,
				"notdone": "has not completed",
			} {
				rs := h.restore(name)
				Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed), name)
				Expect(rs.Status.Message).To(ContainSubstring(want), name)
				Expect(rs.Status.CleanedUp).To(BeTrue(), "nothing ran")
				Expect(controllerutil.ContainsFinalizer(rs, odoov1.RestoreFinalizer)).To(BeFalse())
			}
			Expect(h.replicas()).To(Equal(int32(1)), "Odoo was never stopped")
		})

		It("restores a snapshot by ID from the instance's store", func() {
			h = newBackupHarness()
			h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.SnapshotID = "deadbeef" })
			_, od := h.reconcile()
			Expect(jobArgs(h.job(od.Status.CurrentOperation.Job))).To(ContainSubstring("--snapshot deadbeef"))
		})

		It("is let go when deleted before it ran", func() {
			h = newBackupHarness()
			h.completedBackup()
			od := h.get()
			// A pod still running keeps the restore in Stopping.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: h.name + "-odoo", Namespace: ns, Labels: od.GetServiceSelectorLabels()},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "odoo", Image: "odoo:a"}}},
			}
			h.track(pod)
			rs := h.newRestore("r", restoreFrom("one"))
			_, od = h.reconcile()
			Expect(h.restore("r").Status.Phase).To(Equal(odoov1.RestorePhaseStopping))
			Expect(od.Status.CurrentOperation.Job).To(BeEmpty())

			Expect(k8sClient.Delete(ctx, rs)).To(Succeed())
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			Eventually(func() *odoov1.OdooRestore { return h.restore("r") }).Should(BeNil())
			Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
			Eventually(func() int32 { h.reconcile(); return h.replicas() }).Should(Equal(int32(1)))
		})
	})

	Describe("the data a restore replaced", func() {
		// completedRestore runs a restore to completion and returns it.
		completedRestore := func(name, backup string, image string) *odoov1.OdooRestore {
			h.newRestore(name, func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-" + backup })
			_, od := h.reconcile()
			Expect(od.Status.CurrentOperation.Name).To(Equal(h.name + "-" + name))
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, LiveChanged: true, PreviousKept: true, Image: image})
			h.reconcile()
			rs := h.restore(name)
			Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
			// Suffixes are per second.
			time.Sleep(time.Until(rs.Status.StartedAt.Add(1100 * time.Millisecond)))
			return rs
		}

		It("is deleted when keepPrevious has passed", func() {
			h = newBackupHarness()
			h.completedBackup()
			rs := completedRestore("r", "one", "odoo:a")
			_, od := h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty(), "kept for 72h")

			past := metav1.NewTime(time.Now().Add(-time.Minute))
			rs.Status.Previous.KeptUntil = &past
			Expect(k8sClient.Status().Update(ctx, rs)).To(Succeed())
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationCleanup))
			Expect(od.Status.CurrentOperation.StoppedOdoo).To(BeFalse(), "a cleanup runs next to Odoo")
			Expect(h.replicas()).To(Equal(int32(1)))

			By("a failed cleanup is retried in an hour")
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: false, Step: result.StepCleanup, Error: "busy"})
			_, od = h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
			rs = h.restore("r")
			Expect(rs.Status.RetryAfter.Time).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
			Expect(rs.Status.Previous.State).To(Equal(odoov1.PreviousKept))

			rs.Status.RetryAfter = &past
			Expect(k8sClient.Status().Update(ctx, rs)).To(Succeed())
			_, od = h.reconcile()
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true})
			h.reconcile()
			rs = h.restore("r")
			Expect(rs.Status.Previous.State).To(Equal(odoov1.PreviousRemoved))
			Expect(rs.Status.CleanedUp).To(BeTrue())
			Expect(rs.Status.CleanupAttempts).To(Equal(int32(2)))
			Expect(controllerutil.ContainsFinalizer(rs, odoov1.RestoreFinalizer)).To(BeFalse())
		})

		It("is deleted with its OdooRestore", func() {
			h = newBackupHarness()
			h.completedBackup()
			rs := completedRestore("r", "one", "odoo:a")
			Expect(k8sClient.Delete(ctx, rs)).To(Succeed())
			Expect(h.restore("r")).NotTo(BeNil(), "the finalizer keeps it")
			_, od := h.reconcile()
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationCleanup))
			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true})
			h.reconcile()
			Eventually(func() *odoov1.OdooRestore { return h.restore("r") }).Should(BeNil())
		})

		It("can be put back with an undo, which starts the image it ran", func() {
			h = newBackupHarness()
			h.completedBackup()
			first := completedRestore("r", "one", "odoo:old")
			Expect(h.get().Status.AppliedImage).To(Equal("odoo:old"))

			h.newRestore("undo", func(s *odoov1.OdooRestoreSpec) { s.Undo = first.Name })
			_, od := h.reconcile()
			args := jobArgs(h.job(od.Status.CurrentOperation.Job))
			Expect(args).To(HavePrefix("odoo-backup undo "))
			Expect(args).To(ContainSubstring("--undo-suffix " + first.Status.Suffix))
			undo := h.restore("undo")
			Expect(undo.Status.Suffix).NotTo(Equal(first.Status.Suffix))

			h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, LiveChanged: true, PreviousKept: true})
			_, od = h.reconcile()
			Expect(od.Status.AppliedImage).To(Equal("odoo:a"), "the image the replaced data ran")
			Expect(h.restore("undo").Status.Phase).To(Equal(odoov1.RestorePhaseCompleted))
			Expect(h.restore("undo").Status.Previous.State).To(Equal(odoov1.PreviousKept))
			first = h.restore("r")
			Expect(first.Status.Previous.State).To(Equal(odoov1.PreviousUndone))
			Expect(first.Status.Previous.UndoneBy).To(Equal(h.name + "-undo"))

			By("the undone restore's cleanup only removes its journal")
			Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationCleanup))
			Expect(od.Status.CurrentOperation.Name).To(Equal(first.Name))
		})

		It("refuses undos that cannot be done", func() {
			h = newBackupHarness()
			h.completedBackup()
			older := completedRestore("older", "one", "odoo:a")
			completedRestore("newer", "one", "odoo:a")
			removed := completedRestore("removed", "one", "odoo:a")
			removed.Status.Previous.State = odoov1.PreviousRemoved
			Expect(k8sClient.Status().Update(ctx, removed)).To(Succeed())

			cases := map[string]string{
				"self":     "cannot undo itself",
				"older":    "only the latest restore can be undone",
				"removed":  "no longer kept (removed)",
				"nothing":  "does not exist",
				"inflight": "has not completed",
			}
			h.newRestore("u-self", func(s *odoov1.OdooRestoreSpec) { s.Undo = h.name + "-u-self" })
			h.newRestore("u-older", func(s *odoov1.OdooRestoreSpec) { s.Undo = older.Name })
			h.newRestore("u-removed", func(s *odoov1.OdooRestoreSpec) { s.Undo = removed.Name })
			h.newRestore("u-nothing", func(s *odoov1.OdooRestoreSpec) { s.Undo = h.name + "-nothing" })
			h.newRestore("u-inflight", func(s *odoov1.OdooRestoreSpec) { s.Undo = h.name + "-u-self" })
			for i := 0; i < 8; i++ {
				h.reconcile()
			}
			for name, want := range cases {
				rs := h.restore("u-" + name)
				Expect(rs.Status.Phase).To(Equal(odoov1.RestorePhaseFailed), name)
				Expect(rs.Status.Message).To(ContainSubstring(want), name)
			}
		})
	})

	It("reports a restore whose object vanished mid-swap and keeps Odoo stopped", func() {
		h = newBackupHarness()
		h.completedBackup()
		rs := h.newRestore("r", func(s *odoov1.OdooRestoreSpec) { s.Backup = h.name + "-one" })
		_, od := h.reconcile()
		job := od.Status.CurrentOperation.Job
		// Force-removed (finalizer stripped), then the Job died.
		fresh := h.restore("r")
		controllerutil.RemoveFinalizer(fresh, odoov1.RestoreFinalizer)
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
		Expect(k8sClient.Delete(ctx, rs)).To(Succeed())
		h.finishJob(job, nil)
		_, od = h.reconcile()
		Expect(od.Status.Phase).To(Equal(odoov1.PhaseFailed))
		Expect(condition(od, odoov1.ConditionDegraded).Reason).To(Equal(ReasonRestoreIncomplete))
		Expect(condition(od, odoov1.ConditionDegraded).Message).To(ContainSubstring("Odoo stays stopped"))
		Expect(h.replicas()).To(Equal(int32(0)))
	})

	It("encodes results the controller can read back", func() {
		msg := result.Encode(result.Result{OK: true, SnapshotID: "x"})
		var back map[string]any
		Expect(json.Unmarshal(msg, &back)).To(Succeed())
		Expect(fmt.Sprint(back["ok"])).To(Equal("true"))
	})
})
