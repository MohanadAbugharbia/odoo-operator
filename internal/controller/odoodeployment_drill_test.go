package controller

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// drillJobs are the instance's drill Jobs.
func (h *backupHarness) drillJobs() []batchv1.Job {
	list := &batchv1.JobList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(ns), client.MatchingLabels{
		odoov1.LabelOdooDeployment: h.name, odoov1.LabelJobKind: jobspec.KindDrill,
	})).To(Succeed())
	var live []batchv1.Job
	for _, j := range list.Items {
		if j.DeletionTimestamp.IsZero() {
			live = append(live, j)
		}
	}
	return live
}

// laggingCache is a cache that has not seen the drill Jobs yet.
type laggingCache struct{ client.Client }

func (c laggingCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*batchv1.Job); ok && strings.Contains(key.Name, "-drill-") {
		return apierrors.NewNotFound(batchv1.Resource("jobs"), key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func container(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

var _ = Describe("Restore drills", func() {
	var (
		h     *backupHarness
		clock time.Time
	)
	at := func(s string) time.Time {
		t, err := time.Parse(time.RFC3339, s)
		Expect(err).NotTo(HaveOccurred())
		return t
	}
	// start sets a daily drill at 06:00 UTC and moves the clock to it.
	start := func(mutators ...func(*odoov1.OdooDeployment)) {
		h = newBackupHarness(mutators...)
		clock = at("2026-03-07T10:00:00Z")
		h.r.Now = func() time.Time { return clock }
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Drill.Schedule = "0 6 * * *" })
		_, od := h.reconcile()
		Expect(od.Status.Backup.NextDrillTime.Time).To(BeTemporally("==", at("2026-03-08T06:00:00Z")))
		Expect(od.Status.Backup.DrillDue).To(BeNil())
		clock = at("2026-03-08T06:00:01Z")
	}
	AfterEach(func() {
		if h == nil {
			return
		}
		for _, j := range h.drillJobs() {
			_ = k8sClient.Delete(ctx, &j)
		}
		h.cleanupAll()
		h = nil
	})

	It("restores the newest backup into scratch space with the backup's Odoo image, then records the pass", func() {
		start(func(od *odoov1.OdooDeployment) {
			limit := resource.MustParse("5Gi")
			od.Spec.Backup.Drill.ScratchSizeLimit = &limit
		})
		h.setStoreReady(h.store, true)
		h.completedBackup()
		b := h.backup("one")
		b.Status.Image = "odoo:18.0-backup"
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())

		_, od := h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationDrill))
		Expect(od.Status.CurrentOperation.Name).To(Equal(b.Name))
		Expect(od.Status.Backup.DrillDue.Time).To(BeTemporally("==", at("2026-03-08T06:00:00Z")))
		jobs := h.drillJobs()
		Expect(jobs).To(HaveLen(1))
		job := jobs[0]
		Expect(od.Status.CurrentOperation.Job).To(Equal(job.Name))

		spec := job.Spec.Template.Spec
		scratch := layout.DrillName(od.Status.Database.Name, layout.Suffix(clock))
		load := container(spec.InitContainers, "load")
		Expect(load).NotTo(BeNil())
		Expect(strings.Join(load.Command, " ")).To(ContainSubstring("drill-load --snapshot " + completedSnapshot))
		Expect(strings.Join(load.Command, " ")).To(ContainSubstring("--suffix " + layout.Suffix(clock)))
		odoo := container(spec.InitContainers, "odoo")
		Expect(odoo).NotTo(BeNil())
		Expect(odoo.Image).To(Equal("odoo:18.0-backup"))
		Expect(strings.Join(odoo.Command, " ")).To(ContainSubstring("odoo shell -c /opt/odoo/odoo.conf --no-http --max-cron-threads=0 -d " + scratch))
		Expect(strings.Join(spec.Containers[0].Command, " ")).To(ContainSubstring("drill-finish"))
		Expect(*job.Spec.BackoffLimit).To(BeZero())
		for _, v := range spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				Expect(v.PersistentVolumeClaim.ClaimName).NotTo(Equal(od.GetPvcName()), "a drill never mounts the data volume")
			}
			if v.Name == "drill" {
				Expect(v.EmptyDir.SizeLimit.String()).To(Equal("5Gi"))
			}
		}
		Expect(h.replicas()).To(Equal(int32(1)), "Odoo keeps running")

		By("reconciling while it runs starts nothing else")
		h.reconcile()
		Expect(h.drillJobs()).To(HaveLen(1))

		By("a passed drill is recorded and its Job deleted")
		h.finishJob(job.Name, &result.Result{OK: true, OdooVersion: "18.0", MissingModules: []string{"sale"}, Warnings: []string{"3 attachment(s) were already missing"}})
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		d := od.Status.Backup.LastDrill
		Expect(d).NotTo(BeNil())
		Expect(d.Succeeded).To(BeTrue())
		Expect(d.Backup).To(Equal(b.Name))
		Expect(d.SnapshotID).To(Equal(completedSnapshot))
		Expect(d.Message).To(ContainSubstring("Odoo read it"))
		Expect(d.Message).To(ContainSubstring("lacks modules sale"))
		Expect(d.Message).To(ContainSubstring("already missing"))
		Expect(d.Job).To(BeEmpty())
		Expect(d.CompletedAt).NotTo(BeNil())
		Expect(od.Status.Backup.LastSuccessfulDrillTime).To(Equal(d.CompletedAt))
		Expect(od.Status.Backup.DrillDue).To(BeNil())
		Eventually(h.drillJobs).Should(BeEmpty())
	})

	It("keeps a failed drill's Job and says why", func() {
		start()
		h.setStoreReady(h.store, true)
		h.completedBackup()
		h.reconcile()
		job := h.drillJobs()[0]
		scratch := layout.DrillName(h.dbName(), layout.Suffix(clock))
		h.fake.Seed(scratch, "")
		h.finishJob(job.Name, &result.Result{OK: false, Step: result.StepOdoo, Error: "Odoo could not load the restored database (exit code 255)"})
		_, od := h.reconcile()
		Expect(h.fake.Has(scratch)).To(BeFalse(), "a pod killed before its last step leaves the scratch database")
		d := od.Status.Backup.LastDrill
		Expect(d.Succeeded).To(BeFalse())
		Expect(d.Job).To(Equal(job.Name))
		Expect(d.Message).To(ContainSubstring("failed at step odoo"))
		Expect(d.Message).To(ContainSubstring("--all-containers"))
		Expect(h.drillJobs()).To(HaveLen(1))
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
	})

	It("fails the drill when there is no finished backup", func() {
		start()
		_, od := h.reconcile()
		d := od.Status.Backup.LastDrill
		Expect(d).NotTo(BeNil())
		Expect(d.Succeeded).To(BeFalse())
		Expect(d.Message).To(Equal("there is no finished backup to restore"))
		Expect(od.Status.Backup.DrillDue).To(BeNil())
		Expect(h.drillJobs()).To(BeEmpty())
	})

	It("restores the copy when the backup's own store is not ready", func() {
		start()
		second := h.secondStore()
		h.setStoreReady(h.store, false)
		h.setStoreReady(second, true)
		h.completedBackup()
		b := h.backup("one")
		b.Status.Copies = []odoov1.BackupCopy{{Store: second, SnapshotID: "fedcba9876543210"}}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		h.reconcile()
		jobs := h.drillJobs()
		Expect(jobs).To(HaveLen(1))
		load := container(jobs[0].Spec.Template.Spec.InitContainers, "load")
		Expect(strings.Join(load.Command, " ")).To(ContainSubstring("--snapshot fedcba9876543210"))
		var claims []string
		for _, v := range jobs[0].Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claims = append(claims, v.PersistentVolumeClaim.ClaimName)
			}
		}
		Expect(claims).To(Equal([]string{"offsite"}), "only the copy's store is mounted")
	})

	It("passes over a zip copy for a restic copy", func() {
		start()
		zips, second := h.zipStore(), h.secondStore()
		h.setStoreReady(h.store, false)
		h.setStoreReady(zips, true)
		h.setStoreReady(second, true)
		h.completedBackup()
		b := h.backup("one")
		b.Status.Copies = []odoov1.BackupCopy{
			{Store: zips, File: ns + "/x/prod_2026-10-18_01-00-00.zip"},
			{Store: second, SnapshotID: "fedcba9876543210"},
		}
		Expect(k8sClient.Status().Update(ctx, b)).To(Succeed())
		h.reconcile()
		jobs := h.drillJobs()
		Expect(jobs).To(HaveLen(1))
		load := container(jobs[0].Spec.Template.Spec.InitContainers, "load")
		Expect(strings.Join(load.Command, " ")).To(ContainSubstring("--snapshot fedcba9876543210"))
	})

	It("waits for a backup in the queue, then drills it", func() {
		start()
		h.setStoreReady(h.store, true)
		h.completedBackup()
		h.newBackup("two")
		_, od := h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationBackup))
		Expect(od.Status.Backup.DrillDue).NotTo(BeNil(), "the drill waits")
		Expect(h.drillJobs()).To(BeEmpty())

		h.finishJob(od.Status.CurrentOperation.Job, &result.Result{OK: true, SnapshotID: "1111222233334444"})
		h.reconcile() // the backup finishes
		_, od = h.reconcile()
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationDrill))
		Expect(od.Status.CurrentOperation.Name).To(Equal(h.name+"-two"), "the newest backup")
	})

	It("forgets a waiting drill when drills are turned off", func() {
		start()
		h.setStoreReady(h.store, true)
		h.completedBackup()
		h.newBackup("two")
		_, od := h.reconcile()
		Expect(od.Status.Backup.DrillDue).NotTo(BeNil())
		h.setBackupSpec(func(b *odoov1.OdooBackupConfig) { b.Drill.Schedule = "" })
		_, od = h.reconcile()
		Expect(od.Status.Backup.DrillDue).To(BeNil())
		Expect(od.Status.Backup.NextDrillTime).To(BeNil())
	})

	It("follows a drill Job the cache has not seen yet", func() {
		start()
		h.setStoreReady(h.store, true)
		h.completedBackup()
		h.r.Client = laggingCache{k8sClient}
		_, od := h.reconcile()
		h.r.Client = k8sClient
		Expect(od.Status.CurrentOperation.Kind).To(Equal(odoov1.OperationDrill))
		Expect(od.Status.Backup.LastDrill).To(BeNil())
		Expect(h.drillJobs()).To(HaveLen(1))
	})

	It("ends a drill whose Job was deleted", func() {
		start()
		h.setStoreReady(h.store, true)
		h.completedBackup()
		h.reconcile()
		job := h.drillJobs()[0]
		scratch := layout.DrillName(h.dbName(), layout.Suffix(clock))
		h.fake.Seed(scratch, "")
		Expect(k8sClient.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		Eventually(func() *batchv1.Job { return h.job(job.Name) }).Should(BeNil())
		_, od := h.reconcile()
		Expect(h.fake.Has(scratch)).To(BeFalse())
		Expect(od.Status.CurrentOperation.Kind).To(BeEmpty())
		Expect(od.Status.Backup.LastDrill.Succeeded).To(BeFalse())
		Expect(od.Status.Backup.LastDrill.Message).To(ContainSubstring("was deleted before it finished"))
	})

	It("leaves no scratch database behind when the instance is deleted", func() {
		start()
		live := h.dbName()
		left := []string{layout.DrillName(live, "20260301T060000"), layout.DrillName(live, "20260302T060000")}
		for _, name := range left {
			h.fake.Seed(name, "")
		}
		foreign := layout.DrillName(live, "20260303T060000")
		h.fake.Seed(foreign, "odoo-operator:elsewhere/odoo")
		unrelated := live + "_drill_notes"
		h.fake.Seed(unrelated, "")

		Expect(k8sClient.Delete(ctx, h.get())).To(Succeed())
		for i := 0; i < 3 && h.get() != nil; i++ {
			h.reconcile()
		}
		Expect(h.get()).To(BeNil())
		for _, name := range left {
			Expect(h.fake.Has(name)).To(BeFalse(), name)
		}
		Expect(h.fake.Has(foreign)).To(BeTrue(), "a database another owner tagged stays")
		Expect(h.fake.Has(unrelated)).To(BeTrue(), "only the names drills make are dropped")
	})
})
