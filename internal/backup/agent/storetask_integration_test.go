package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/retention"
)

func TestStoreCheckAndPrune(t *testing.T) {
	f := newFixture(t)
	f.seed(20)
	scheduled := func(o *BackupOptions) { o.Kind = retention.KindScheduled }
	f.mustBackup("one", scheduled)
	// New attachments, so the forgotten backup holds data no other does.
	for i := 0; i < 10; i++ {
		f.addAttachment(randomHex(t, 4), randomContent(1000+i))
	}
	f.mustBackup("two", scheduled)

	base := *f.env.Restic
	base.Env = nil
	for _, kv := range f.env.Restic.Env {
		if !strings.HasPrefix(kv, "RESTIC_REPOSITORY=") {
			base.Env = append(base.Env, kv)
		}
	}
	repos := []Repository{
		{Name: "shop/my-odoo", URL: f.repo},
		{Name: "shop/never-backed-up", URL: filepath.Join(t.TempDir(), "absent")},
	}
	run := func(task, readData string) (ok bool, stored int64, errs []string, repositories int) {
		res := StoreTask(context.Background(), &base, f.env.Log, StoreTaskOptions{Task: task, Repositories: repos, ReadData: readData})
		return res.OK, res.StoredBytes, res.RepositoryErrors, res.Repositories
	}

	ok, before, errs, n := run(TaskCheck, "1/3")
	if !ok || n != 1 || before == 0 {
		t.Fatalf("check: ok %v, %d repositories, %d bytes, %v", ok, n, before, errs)
	}

	f.mustBackup("three", func(o *BackupOptions) {
		scheduled(o)
		o.Retention = &Retention{Policy: retention.Policy{Last: 1}}
	})
	ok, after, errs, _ := run(TaskPrune, "")
	if !ok {
		t.Fatalf("prune: %v", errs)
	}
	if after >= before {
		t.Fatalf("prune freed nothing: %d bytes before, %d after", before, after)
	}
	if ok, _, errs, _ := run(TaskCheck, "all"); !ok {
		t.Fatalf("check after prune: %v", errs)
	}
	if r := f.restore(f.snapshotsByBackup()["three"], f.nextSuffix()); !r.OK {
		t.Fatalf("restore after prune: %+v", r)
	}

	t.Log("damaged data is found by a full check, and named")
	packs, _ := filepath.Glob(filepath.Join(f.repo, "data", "*", "*"))
	if len(packs) == 0 {
		t.Fatal("no pack files")
	}
	for _, p := range packs {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)/2] ^= 0xff
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ok, _, errs, _ = run(TaskCheck, "all")
	if ok || len(errs) != 1 || !strings.HasPrefix(errs[0], "shop/my-odoo: ") {
		t.Fatalf("check of a damaged repository: ok %v, %v", ok, errs)
	}
}

func TestStoreTaskWrongKey(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	f.mustBackup("one")
	base := restic.Restic{Bin: f.env.Restic.Bin, Env: []string{"RESTIC_PASSWORD=wrong", "RESTIC_CACHE_DIR=" + t.TempDir()}}
	res := StoreTask(context.Background(), &base, f.env.Log, StoreTaskOptions{
		Task: TaskCheck, Repositories: []Repository{{Name: "shop/my-odoo", URL: f.repo}},
	})
	if res.OK || !strings.Contains(res.Error, "wrong password") {
		t.Fatalf("check with the wrong key: %+v", res)
	}
}
