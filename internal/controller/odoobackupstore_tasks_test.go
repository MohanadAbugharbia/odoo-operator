package controller

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/jobspec"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
)

// taskJobs are the store's check and prune Jobs.
func (s *storeHarness) taskJobs() []batchv1.Job {
	var out []batchv1.Job
	for _, j := range s.jobs() {
		if k := j.Labels[odoov1.LabelJobKind]; k == jobspec.KindCheck || k == jobspec.KindPrune {
			out = append(out, j)
		}
	}
	return out
}

// makeReady runs the first probe to success.
func (s *storeHarness) makeReady() {
	s.reconcile()
	probe := s.get().Status.ProbeJob
	ExpectWithOffset(1, probe).NotTo(BeEmpty())
	s.finishJob(probe, true)
	s.podReporting(probe, `{"ok":true}`, 0)
	s.reconcile()
	ExpectWithOffset(1, s.get().Status.Ready).To(BeTrue())
}

func (s *storeHarness) setSpec(mutate func(*odoov1.OdooBackupStoreSpec)) {
	store := s.get()
	mutate(&store.Spec)
	ExpectWithOffset(1, k8sClient.Update(ctx, store)).To(Succeed())
}

func command(job batchv1.Job) string {
	return strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")
}

var _ = Describe("OdooBackupStore check and prune", func() {
	var (
		t     *bwTracker
		s     *storeHarness
		clock time.Time
		od    *odoov1.OdooDeployment
	)

	at := func(v string) time.Time {
		tm, err := time.Parse(time.RFC3339, v)
		Expect(err).NotTo(HaveOccurred())
		return tm
	}

	BeforeEach(func() {
		t = &bwTracker{}
		s = newStoreHarness(t)
		clock = at("2026-03-07T10:00:00Z")
		s.r.Now = func() time.Time { return clock }
		s.createPVCStore()
		s.makeReady()
		od = newHarness().spec(func(o *odoov1.OdooDeployment) { o.Spec.Backup.Stores = []string{s.name} })
		t.create(od)
		s.events()
	})
	AfterEach(func() {
		for _, j := range s.jobs() {
			t.track(j.DeepCopy())
		}
		t.cleanup()
	})

	It("prunes then checks, one Job at a time, each part of the data in turn", func() {
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) {
			sp.Check.Schedule, sp.Prune.Schedule, sp.TimeZone = "0 5 * * *", "0 5 * * *", "Europe/Berlin"
		})
		res := s.reconcile()
		Expect(s.taskJobs()).To(BeEmpty(), "a new schedule does not run for times before it")
		st := s.get().Status
		Expect(st.NextCheckTime.Time).To(BeTemporally("==", at("2026-03-08T04:00:00Z")), "05:00 in Berlin")
		Expect(st.NextPruneTime.Time).To(BeTemporally("==", at("2026-03-08T04:00:00Z")))
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", 18*time.Hour+time.Second))

		By("at the time the prune starts first")
		clock = at("2026-03-08T04:00:10Z")
		s.reconcile()
		jobs := s.taskJobs()
		Expect(jobs).To(HaveLen(1))
		prune := jobs[0]
		Expect(prune.Labels[odoov1.LabelJobKind]).To(Equal(jobspec.KindPrune))
		repo := layout.Repository(ns, od.Name)
		Expect(command(prune)).To(ContainSubstring(" prune "))
		Expect(command(prune)).To(ContainSubstring("--repository " + repo + "="))
		Expect(metav1.IsControlledBy(&prune, s.get())).To(BeTrue())
		st = s.get().Status
		Expect(st.TaskJob).To(Equal(prune.Name))
		Expect(st.TaskKind).To(Equal(jobspec.KindPrune))

		By("the check waits while the prune runs")
		s.reconcile()
		Expect(s.taskJobs()).To(HaveLen(1))

		By("a passed prune is recorded, its Job deleted, and the check starts")
		s.finishJob(prune.Name, true)
		s.podReporting(prune.Name, `{"ok":true,"repositories":1,"storedBytes":2048}`, 0)
		s.reconcile()
		st = s.get().Status
		Expect(st.LastPrune).NotTo(BeNil())
		Expect(st.LastPrune.Result).To(Equal(odoov1.StoreTaskPassed))
		Expect(st.LastPrune.Message).To(ContainSubstring("pruned 1 repositories"))
		Expect(st.LastPrune.Repositories).To(Equal([]string{repo}))
		Expect(st.LastPrune.Job).To(BeEmpty())
		Expect(st.StoredBytes).To(Equal(int64(2048)))
		Expect(st.Repositories).To(Equal(int32(1)))
		Eventually(func() *batchv1.Job { return s.job(prune.Name) }).Should(BeNil())
		jobs = s.taskJobs()
		Expect(jobs).To(HaveLen(1))
		check := jobs[0]
		Expect(check.Labels[odoov1.LabelJobKind]).To(Equal(jobspec.KindCheck))
		Expect(command(check)).To(ContainSubstring("check --read-data 1/7"))
		Expect(s.events()).To(ContainElement(ContainSubstring(ReasonStoreTaskPassed)))

		By("a failed check keeps its Job and warns")
		s.finishJob(check.Name, false)
		s.podReporting(check.Name, `{"ok":false,"step":"store-task","error":"`+repo+`: pack is damaged"}`, 1)
		s.reconcile()
		st = s.get().Status
		Expect(st.LastCheck.Result).To(Equal(odoov1.StoreTaskFailed))
		Expect(st.LastCheck.Message).To(ContainSubstring("pack is damaged"))
		Expect(st.LastCheck.Job).To(Equal(check.Name))
		Expect(st.LastCheck.Part).To(Equal("1/7"))
		Expect(st.TaskJob).To(BeEmpty())
		Expect(s.job(check.Name)).NotTo(BeNil())
		Expect(s.events()).To(ContainElement(ContainSubstring(ReasonStoreCheckFailed)))

		By("the next day's check reads the next part and replaces the failed Job")
		clock = at("2026-03-09T04:00:10Z")
		s.reconcile()
		prune = *s.job(s.get().Status.TaskJob)
		Expect(prune.Labels[odoov1.LabelJobKind]).To(Equal(jobspec.KindPrune))
		s.finishJob(prune.Name, true)
		s.podReporting(prune.Name, `{"ok":true,"repositories":1,"storedBytes":1024}`, 0)
		s.reconcile()
		var next *batchv1.Job
		for _, j := range s.taskJobs() {
			if j.Labels[odoov1.LabelJobKind] == jobspec.KindCheck {
				next = j.DeepCopy()
			}
		}
		Expect(next).NotTo(BeNil())
		Expect(next.Name).NotTo(Equal(check.Name))
		Expect(command(*next)).To(ContainSubstring("--read-data 2/7"))
		Eventually(func() *batchv1.Job { return s.job(check.Name) }).Should(BeNil())
	})

	It("records a pass without a Job when no instance uses the store", func() {
		od.Spec.Backup.Stores = nil
		Expect(k8sClient.Delete(ctx, od)).To(Succeed())
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) { sp.Check.Schedule = "@daily" })
		s.reconcile()
		clock = at("2026-03-08T00:00:05Z")
		s.reconcile()
		Expect(s.taskJobs()).To(BeEmpty())
		st := s.get().Status
		Expect(st.LastCheck).NotTo(BeNil())
		Expect(st.LastCheck.Result).To(Equal(odoov1.StoreTaskPassed))
		Expect(st.LastCheck.Message).To(Equal("no instance backs up to this store"))
		Expect(st.LastCheckScheduleTime.Time).To(BeTemporally("==", at("2026-03-08T00:00:00Z")))
	})

	It("runs a missed time once, and nothing more", func() {
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) { sp.Prune.Schedule = "0 * * * *" })
		s.reconcile()
		clock = clock.Add(5 * time.Hour)
		s.reconcile()
		Expect(s.taskJobs()).To(HaveLen(1))
		Expect(s.get().Status.LastPruneScheduleTime.Time).To(BeTemporally("==", at("2026-03-07T15:00:00Z")))
	})

	It("reports an invalid schedule and runs the valid one", func() {
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) {
			sp.Check.Schedule, sp.Prune.Schedule = "not a schedule", "@daily"
		})
		s.reconcile()
		store := s.get()
		c := meta.FindStatusCondition(store.Status.Conditions, ConditionStoreSchedules)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Message).To(ContainSubstring("spec.check.schedule"))
		Expect(store.Status.NextCheckTime).To(BeNil())
		Expect(store.Status.NextPruneTime).NotTo(BeNil())
		Expect(s.events()).To(HaveLen(1))
		s.reconcile()
		Expect(s.events()).To(BeEmpty(), "warns once")

		By("fixing it clears the condition")
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) { sp.Check.Schedule = "@weekly" })
		s.reconcile()
		Expect(meta.FindStatusCondition(s.get().Status.Conditions, ConditionStoreSchedules)).To(BeNil())
	})

	It("does not start a task while the store is unreachable", func() {
		s.setSpec(func(sp *odoov1.OdooBackupStoreSpec) { sp.Check.Schedule = "@daily" })
		s.reconcile()
		store := s.get()
		store.Status.Ready = false
		Expect(k8sClient.Status().Update(ctx, store)).To(Succeed())
		clock = at("2026-03-08T00:00:05Z")
		s.reconcile()
		Expect(s.taskJobs()).To(BeEmpty())
	})
})
