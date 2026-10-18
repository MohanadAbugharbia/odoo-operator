package agent

import (
	"context"
	"sort"
	"testing"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/retention"
)

// snapshotsByBackup lists the instance's snapshots as backup name → ID.
func (f *fixture) snapshotsByBackup() map[string]string {
	f.t.Helper()
	host := layout.Host(itNamespace, itDeployment)
	snaps, err := f.env.Restic.Snapshots(context.Background(), host, layout.TagDeployment+host)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range snaps {
		out[s.TagValue(layout.TagBackup)] = s.ID
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBackupAppliesRetention(t *testing.T) {
	f := newFixture(t)
	f.seed(3)
	scheduled := func(o *BackupOptions) { o.Kind = retention.KindScheduled }
	f.mustBackup("manual-1")
	f.mustBackup("kept", scheduled)
	f.mustBackup("s1", scheduled)
	ids := f.snapshotsByBackup()

	ret := &Retention{Policy: retention.Policy{Last: 1}, ProtectBackups: []string{"kept"}}
	withRetention := func(o *BackupOptions) { scheduled(o); o.Retention = ret }
	res := f.mustBackup("s2", withRetention)
	if len(res.Forgotten) != 1 || res.Forgotten[0] != ids["s1"] {
		t.Fatalf("forgotten %v, want s1's snapshot %s", res.Forgotten, ids["s1"])
	}
	if res.RetentionError != "" {
		t.Fatalf("retention error: %s", res.RetentionError)
	}
	if got := keys(f.snapshotsByBackup()); len(got) != 3 || got[0] != "kept" || got[1] != "manual-1" || got[2] != "s2" {
		t.Fatalf("left %v, want kept, manual-1 (manual 0 keeps all) and s2", got)
	}

	t.Log("the snapshot a restore still needs is kept, and manual backups get their own count")
	ret.Policy.Manual = 1
	ret.ProtectSnapshots = []string{f.snapshotsByBackup()["s2"][:8]}
	f.mustBackup("manual-2")
	res = f.mustBackup("s3", withRetention)
	left := keys(f.snapshotsByBackup())
	if want := []string{"kept", "manual-2", "s2", "s3"}; len(left) != len(want) || left[0] != want[0] || left[1] != want[1] || left[2] != want[2] || left[3] != want[3] {
		t.Fatalf("left %v, want %v (forgotten %v)", left, want, res.Forgotten)
	}

	t.Log("a restore from what is left still works")
	if r := f.restore(f.snapshotsByBackup()["s3"], f.nextSuffix()); !r.OK {
		t.Fatalf("restore after retention: %+v", r)
	}
}

func TestRetentionFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	res := f.mustBackup("s1", func(o *BackupOptions) { o.Kind = retention.KindScheduled })
	broken := *f.env.Restic
	broken.Env = append(append([]string(nil), broken.Env...), "RESTIC_REPOSITORY=/nonexistent/repo")
	_, err := applyRetention(context.Background(), f.env, &broken, layout.Host(itNamespace, itDeployment), res.SnapshotID,
		&Retention{Policy: retention.Policy{Last: 1}})
	if err == nil {
		t.Fatal("retention against a missing repository should fail")
	}
	if _, ok := f.snapshotsByBackup()["s1"]; !ok {
		t.Fatal("the backup is still there")
	}
}

func TestFailedCopyLeavesTheBackup(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	broken := *f.env.Restic
	broken.Env = append(append([]string(nil), broken.Env...), "RESTIC_REPOSITORY=/proc/no-such-store/repo")
	good := *f.env.Restic
	good.Env = append(append([]string(nil), good.Env...), "RESTIC_REPOSITORY="+t.TempDir()+"/copy", "RESTIC_PASSWORD=other")
	res := f.mustBackup("one", func(o *BackupOptions) {
		o.Copies = []CopyTarget{{Store: "gone", Restic: &broken}, {Store: "fine", Restic: &good}}
	})
	if len(res.Copies) != 2 {
		t.Fatalf("copies %+v", res.Copies)
	}
	if res.Copies[0].Error == "" || res.Copies[0].SnapshotID != "" {
		t.Fatalf("the unreachable store reports an error: %+v", res.Copies[0])
	}
	if res.Copies[1].Error != "" || res.Copies[1].SnapshotID == "" {
		t.Fatalf("the next store is still copied to: %+v", res.Copies[1])
	}

	t.Log("copying the same backup again finds the copy instead of making another")
	id, err := copySnapshot(context.Background(), f.env, CopyTarget{Store: "fine", Restic: &good},
		layout.Host(itNamespace, itDeployment), "one", res.SnapshotID, nil)
	if err != nil || id != res.Copies[1].SnapshotID {
		t.Fatalf("second copy: %s, %v (want %s)", id, err, res.Copies[1].SnapshotID)
	}
}
