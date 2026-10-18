package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// writeConf writes an odoo.conf whose database cannot be reached.
func writeConf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "odoo.conf")
	conf := "[options]\ndb_host = 127.0.0.1\ndb_port = 1\ndb_user = odoo\ndb_password = pw\n" +
		"db_name = prod\ndb_sslmode = disable\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readResult(t *testing.T, path string) result.Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := result.Decode(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunUsage(t *testing.T) {
	ctx := context.Background()
	if code := run(ctx, "version", nil); code != 0 {
		t.Errorf("version = %d", code)
	}
	for _, cmd := range []string{"", "help", "nope", "--version", "BACKUP"} {
		if code := run(ctx, cmd, nil); code != 2 {
			t.Errorf("run(%q) = %d, want 2", cmd, code)
		}
	}
}

func TestRunBadFlags(t *testing.T) {
	ctx := context.Background()
	for _, cmd := range []string{"backup", "restore", "undo", "cleanup", "prepare", "probe"} {
		if code := run(ctx, cmd, []string{"--no-such-flag"}); code != 2 {
			t.Errorf("%s with an unknown flag = %d, want 2", cmd, code)
		}
	}
	if code := run(ctx, "backup", []string{"--retry-lock", "soon"}); code != 2 {
		t.Errorf("bad duration = %d", code)
	}
}

func TestRunBackupRequiresNames(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	for _, args := range [][]string{
		nil,
		{"--namespace", "ns", "--deployment", "dep"},
		{"--namespace", "ns", "--backup", "b"},
		{"--deployment", "dep", "--backup", "b"},
	} {
		if code := run(context.Background(), "backup", append(args, "--termination-log", log)); code != 2 {
			t.Errorf("backup %q = %d, want 2", args, code)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("a usage error wrote a result")
	}
}

func TestRunWritesResultOnFailure(t *testing.T) {
	conf := writeConf(t)
	names := []string{"--namespace", "ns", "--deployment", "d", "--backup", "b"}
	backup := func(args ...string) []string { return append(append([]string(nil), names...), args...) }
	const suffix = "20250101T000000"
	cases := []struct {
		name, cmd string
		args      []string
		step      string
	}{
		{"backup, missing config", "backup", backup("--config", "/nonexistent/odoo.conf"), result.StepConnect},
		{"backup, bad majors", "backup", backup("--config", conf, "--pg-majors", "16,x"), result.StepConnect},
		{"backup, unreachable database", "backup", backup("--config", conf), result.StepConnect},
		{"restore, missing config", "restore", []string{"--config", "/nonexistent", "--suffix", suffix}, result.StepConnect},
		{"restore, invalid suffix", "restore", []string{"--config", conf, "--suffix", "bad"}, result.StepConnect},
		{"restore, unreachable database", "restore",
			[]string{"--config", conf, "--suffix", suffix, "--snapshot", "x", "--data", t.TempDir(),
				"--expect-modules", " sale, ,stock "}, result.StepConnect},
		{"undo, invalid undo suffix", "undo", []string{"--config", conf, "--suffix", suffix, "--undo-suffix", "x"},
			result.StepConnect},
		{"cleanup, missing config", "cleanup", []string{"--config", "/nonexistent", "--suffix", suffix}, result.StepConnect},
		{"cleanup, invalid suffix", "cleanup", []string{"--config", conf, "--suffix", "../x"}, result.StepCleanup},
		{"cleanup, unreachable database", "cleanup",
			[]string{"--config", conf, "--suffix", suffix, "--data", t.TempDir()}, result.StepConnect},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "termination-log")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if code := run(ctx, c.cmd, append(c.args, "--termination-log", log)); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			r := readResult(t, log)
			if r.OK || r.Step != c.step || r.Error == "" {
				t.Fatalf("result = %+v", r)
			}
			if strings.Contains(r.Error, "pw") && strings.Contains(r.Error, "db_password") {
				t.Fatalf("the result leaks the password: %s", r.Error)
			}
		})
	}
}

func TestRunPrepare(t *testing.T) {
	data := t.TempDir()
	if code := run(context.Background(), "prepare", []string{"--data", data}); code != 2 {
		t.Fatalf("prepare without --database = %d", code)
	}
	if code := run(context.Background(), "prepare", []string{"--data", data, "--database", "prod"}); code != 0 {
		t.Fatalf("prepare = %d", code)
	}
	if st, err := os.Stat(filepath.Join(data, "filestore", "prod")); err != nil || !st.IsDir() {
		t.Fatalf("filestore folder: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), "prepare", []string{"--data", blocker, "--database", "prod"}); code != 1 {
		t.Fatalf("prepare below a file = %d", code)
	}
}

func TestRunProbe(t *testing.T) {
	bin := t.TempDir()
	rclone := `#!/bin/sh
case "$5" in
  rcat) cat > "$6" ;;
  cat) cat "$6" ;;
  deletefile) rm "$6" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(rclone), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	if code := run(context.Background(), "probe", []string{"--target", target, "--termination-log", log}); code != 0 {
		t.Fatalf("probe = %d: %+v", code, readResult(t, log))
	}
	if r := readResult(t, log); !r.OK {
		t.Fatalf("result = %+v", r)
	}
	// An unwritable target fails with a result.
	code := run(context.Background(), "probe", []string{"--target", "/nonexistent/dir", "--termination-log", log})
	if code != 1 {
		t.Fatalf("probe of a missing folder = %d", code)
	}
	if r := readResult(t, log); r.OK || r.Step != result.StepProbe {
		t.Fatalf("result = %+v", r)
	}
}

func TestCommonEnv(t *testing.T) {
	conf := writeConf(t)
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	var c common
	c.register(fs)
	err := fs.Parse([]string{
		"--config", conf, "--maintenance-db", "maint", "--pg-bin-template", "/usr/lib/postgresql/%d/bin",
		"--pg-majors", " 16, ,17,18 ", "--restic", "/bin/restic", "--restic-option", "a=1", "--restic-option", "b=2",
		"--retry-lock", "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.resticOptions.String() != "a=1,b=2" {
		t.Fatalf("restic options %q", c.resticOptions.String())
	}
	e, err := c.env()
	if err != nil {
		t.Fatal(err)
	}
	if e.Config.Name != "prod" || e.Config.Port != 1 || e.MaintenanceDB != "maint" {
		t.Fatalf("env = %+v", e)
	}
	if e.Clients.BinDirTemplate != "/usr/lib/postgresql/%d/bin" ||
		!reflect.DeepEqual(e.Clients.Majors, []int{16, 17, 18}) {
		t.Fatalf("clients = %+v", e.Clients)
	}
	if e.Restic.Bin != "/bin/restic" || !reflect.DeepEqual(e.Restic.Options, []string{"a=1", "b=2"}) ||
		e.Restic.RetryLock != 5*time.Minute {
		t.Fatalf("restic = %+v", e.Restic)
	}
	if e.Log == nil || e.Restic.Log == nil {
		t.Fatal("no log")
	}
}

func TestCommonDefaults(t *testing.T) {
	t.Setenv("ODOO_BACKUP_PG_BIN_TEMPLATE", "/opt/pg%d")
	t.Setenv("ODOO_BACKUP_PG_MAJORS", "")
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	var c common
	c.register(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c.config != "/etc/odoo/odoo.conf" || c.maintenanceDB != "postgres" || c.pgBinTemplate != "/opt/pg%d" ||
		c.pgMajors != "13,14,15,16,17,18" || c.resticBin != "restic" || c.retryLock != 31*time.Minute ||
		c.terminationLog != "/dev/termination-log" {
		t.Fatalf("defaults = %+v", c)
	}
	t.Setenv("ODOO_BACKUP_PG_MAJORS", "17")
	if envOr("ODOO_BACKUP_PG_MAJORS", "x") != "17" || envOr("ODOO_BACKUP_UNSET_FOR_TEST", "fb") != "fb" {
		t.Fatal("envOr")
	}
}

func TestFinish(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	if code := finish(log, result.Result{OK: true}); code != 0 {
		t.Fatalf("ok = %d", code)
	}
	if code := finish(log, result.Result{OK: true, Deferred: true}); code != 0 {
		t.Fatalf("deferred = %d", code)
	}
	if r := readResult(t, log); !r.Deferred {
		t.Fatalf("result = %+v", r)
	}
	if code := finish(log, result.Result{Step: "x", Error: "y"}); code != 1 {
		t.Fatalf("failed = %d", code)
	}
	// An unwritable log does not change the exit code.
	if code := finish("/nonexistent/dir/log", result.Result{OK: true}); code != 0 {
		t.Fatalf("unwritable log = %d", code)
	}
}

// TestMainProcess runs main() in a child process, for its exit codes.
func TestMainProcess(t *testing.T) {
	if os.Getenv("ODOO_BACKUP_TEST_MAIN") == "1" {
		args := strings.Fields(os.Getenv("ODOO_BACKUP_TEST_ARGS"))
		os.Args = append([]string{"odoo-backup"}, args...)
		main()
		return
	}
	cases := []struct {
		args string
		code int
		out  string
	}{
		{"", 2, ""},
		{"version", 0, "dev\n"},
		{"frobnicate", 2, ""},
	}
	for _, c := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainProcess$")
		cmd.Env = append(os.Environ(), "ODOO_BACKUP_TEST_MAIN=1", "ODOO_BACKUP_TEST_ARGS="+c.args)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != c.code {
			t.Errorf("odoo-backup %s: exit %d, want %d", c.args, code, c.code)
		}
		if c.out != "" && !strings.HasPrefix(string(out), c.out) {
			t.Errorf("odoo-backup %s printed %q", c.args, out)
		}
		if c.code == 2 && !strings.Contains(stderr.String(), "usage: odoo-backup") {
			t.Errorf("odoo-backup %s: no usage on stderr: %q", c.args, stderr.String())
		}
	}
}

func TestRunExport(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{
		nil,
		{"--output", "x.zip", "--target", "remote:"},
		{"--no-such-flag"},
	} {
		if code := run(ctx, "export", args); code != 2 {
			t.Errorf("export %q = %d, want 2", args, code)
		}
	}
	// Without a repository the snapshot cannot be found; nothing is written.
	t.Setenv("RESTIC_REPOSITORY", filepath.Join(t.TempDir(), "none"))
	t.Setenv("RESTIC_PASSWORD", "x")
	out := filepath.Join(t.TempDir(), "out.zip")
	log := filepath.Join(t.TempDir(), "log")
	if code := run(ctx, "export", []string{"--output", out, "--termination-log", log}); code != 1 {
		t.Fatalf("export from a missing repository = %d, want 1", code)
	}
	if r := readResult(t, log); r.OK || r.Step != result.StepFetch {
		t.Fatalf("result = %+v", r)
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 0 {
		t.Fatalf("a failed export left %v", entries)
	}
}
