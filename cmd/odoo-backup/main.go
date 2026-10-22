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

// Command odoo-backup runs inside the operator's backup, restore, cleanup
// and probe Jobs (the odoo-operator-backup image). It writes its outcome as
// JSON to the termination message, where the operator reads it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/agent"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
	"github.com/MohanadAbugharbia/odoo-operator/internal/schedule"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: odoo-backup <command> [flags]

commands:
  backup    take a backup of the instance in odoo.conf
  restore   restore a snapshot in place (Odoo must be stopped)
  undo      put back the data an earlier restore replaced
  cleanup   delete the data a restore replaced
  prepare   create the instance's filestore folder if it is missing
  probe     check that a store can be written, read and deleted from
  drill-load    restore a snapshot into scratch space and check it (drill, step 1)
  drill-finish  drop the drill's scratch database and report the drill (step 3)
  check     check the store's repositories (restic check)
  prune     free the space of forgotten snapshots (restic prune)
  export    write a snapshot as the zip Odoo's database manager makes
  version   print the version
`

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// common are the flags every command that talks to the database takes.
type common struct {
	config         string
	maintenanceDB  string
	pgBinTemplate  string
	pgMajors       string
	resticBin      string
	resticOptions  stringList
	retryLock      time.Duration
	noLock         bool
	terminationLog string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", layout.ConfigMount+"/odoo.conf", "odoo.conf of the instance")
	fs.StringVar(&c.maintenanceDB, "maintenance-db", "postgres", "database to run CREATE/DROP/RENAME DATABASE from")
	fs.StringVar(&c.pgBinTemplate, "pg-bin-template", os.Getenv("ODOO_BACKUP_PG_BIN_TEMPLATE"),
		"folder of each PostgreSQL client major, with %d for the major; empty uses PATH")
	fs.StringVar(&c.pgMajors, "pg-majors", envOr("ODOO_BACKUP_PG_MAJORS", "13,14,15,16,17,18"),
		"client majors to look for")
	fs.StringVar(&c.resticBin, "restic", "restic", "restic executable")
	fs.Var(&c.resticOptions, "restic-option", "restic -o option (repeatable)")
	fs.DurationVar(&c.retryLock, "retry-lock", 31*time.Minute, "how long restic waits for a repository lock")
	fs.BoolVar(&c.noLock, "no-lock", false, "read the repository without locking it (a read-only store)")
	fs.StringVar(&c.terminationLog, "termination-log", layout.TerminationLog, "where the result is written")
}

func (c *common) env() (*agent.Env, error) {
	var cfg odooconf.Config
	if c.config != "" {
		var err error
		if cfg, err = odooconf.Load(c.config); err != nil {
			return nil, err
		}
	}
	var majors []int
	for _, m := range strings.Split(c.pgMajors, ",") {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		n, err := strconv.Atoi(m)
		if err != nil {
			return nil, fmt.Errorf("--pg-majors: %q is not a number", m)
		}
		majors = append(majors, n)
	}
	logger := agent.NewLogger(os.Stderr)
	return &agent.Env{
		Config:        cfg,
		MaintenanceDB: c.maintenanceDB,
		Restic: &restic.Restic{
			Bin:       c.resticBin,
			Options:   c.resticOptions,
			RetryLock: c.retryLock,
			NoLock:    c.noLock,
			Log:       os.Stderr,
		},
		Clients: pg.Clients{BinDirTemplate: c.pgBinTemplate, Majors: majors},
		Log:     logger,
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(result.ExitUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Args[1], os.Args[2:]))
}

func run(ctx context.Context, command string, args []string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	var c common
	if err := agent.LimitHostKeyAlgorithms(); err != nil {
		fmt.Fprintln(os.Stderr, "read the SFTP known_hosts:", err)
	}
	if err := agent.UseSSHAuthPrivateKey(); err != nil {
		fmt.Fprintln(os.Stderr, "find the SFTP private key:", err)
	}
	switch command {
	case "version":
		fmt.Println(version)
		return 0
	case "backup":
		return runBackup(ctx, fs, &c, args)
	case "restore", "undo":
		return runRestore(ctx, fs, &c, command, args)
	case "cleanup":
		return runCleanup(ctx, fs, &c, args)
	case "drill-load", "drill-finish":
		return runDrill(ctx, fs, &c, command, args)
	case "prepare":
		dataRoot := fs.String("data", layout.DataMount, "root of the instance's data volume")
		database := fs.String("database", "", "database name (the filestore folder)")
		if err := fs.Parse(args); err != nil {
			return result.ExitUsage
		}
		if *database == "" {
			fmt.Fprintln(os.Stderr, "prepare: --database is required")
			return result.ExitUsage
		}
		if err := agent.Prepare(*dataRoot, *database); err != nil {
			fmt.Fprintln(os.Stderr, "prepare:", err)
			return 1
		}
		return 0
	case "export":
		return runExport(ctx, fs, &c, args)
	case agent.TaskCheck, agent.TaskPrune:
		return runStoreTask(ctx, fs, command, args)
	case "probe":
		target := fs.String("target", "", "the store root, as rclone names it")
		readOnly := fs.Bool("read-only", false, "only list the store (a read-only store)")
		terminationLog := fs.String("termination-log", layout.TerminationLog, "where the result is written")
		if err := fs.Parse(args); err != nil {
			return result.ExitUsage
		}
		return finish(*terminationLog, agent.Probe(ctx, agent.ProbeOptions{Target: *target, ReadOnly: *readOnly}))
	default:
		fmt.Fprint(os.Stderr, usage)
		return result.ExitUsage
	}
}

func runBackup(ctx context.Context, fs *flag.FlagSet, c *common, args []string) int {
	c.register(fs)
	var o agent.BackupOptions
	fs.StringVar(&o.Root, "root", layout.BackupRoot, "folder to back up (holds the filestore mount)")
	fs.StringVar(&o.Namespace, "namespace", "", "namespace of the OdooDeployment")
	fs.StringVar(&o.Deployment, "deployment", "", "name of the OdooDeployment")
	fs.StringVar(&o.Backup, "backup", "", "name of the OdooBackup")
	fs.StringVar(&o.Kind, "kind", "manual", "manual, scheduled or pre-upgrade")
	fs.StringVar(&o.Image, "image", "", "the instance's status.appliedImage")
	fs.StringVar(&o.UpgradeToken, "upgrade-token", "", "the instance's status.appliedUpgradeToken")
	fs.StringVar(&o.Consistency, "consistency", "Online", "Online or Offline")
	fs.BoolVar(&o.WaitForCleanup, "wait-for-cleanup", false, "defer while Odoo's filestore cleanup runs or is due")
	fs.DurationVar(&o.CleanupWindow, "cleanup-window", 10*time.Minute, "how soon a due cleanup defers the backup")
	var ret agent.Retention
	var withRetention bool
	var timeZone string
	var protectBackups, protectSnapshots, copies stringList
	fs.Var(&copies, "copy", "store to copy the backup to (repeatable); the n-th is reached through "+
		"COPY<n>_RESTIC_REPOSITORY and COPY<n>_RESTIC_PASSWORD_FILE")
	fs.BoolVar(&withRetention, "retention", false, "forget the snapshots the keep rules no longer keep after the backup")
	fs.IntVar(&ret.Policy.Last, "keep-last", 0, "keep the newest N scheduled backups")
	fs.IntVar(&ret.Policy.Hourly, "keep-hourly", 0, "keep the newest scheduled backup of each of the last N hours")
	fs.IntVar(&ret.Policy.Daily, "keep-daily", 0, "keep the newest scheduled backup of each of the last N days")
	fs.IntVar(&ret.Policy.Weekly, "keep-weekly", 0, "keep the newest scheduled backup of each of the last N weeks")
	fs.IntVar(&ret.Policy.Monthly, "keep-monthly", 0, "keep the newest scheduled backup of each of the last N months")
	fs.IntVar(&ret.Policy.Yearly, "keep-yearly", 0, "keep the newest scheduled backup of each of the last N years")
	fs.StringVar(&ret.Policy.Within, "keep-within", "", "keep scheduled backups this close to the newest (1y6m, 30d)")
	fs.IntVar(&ret.Policy.Manual, "keep-manual", 0, "keep the newest N manual backups; 0 keeps all")
	fs.IntVar(&ret.Policy.PreUpgrade, "keep-pre-upgrade", 0, "keep the newest N pre-upgrade backups; 0 keeps all")
	fs.StringVar(&timeZone, "time-zone", "UTC", "IANA time zone the periods are counted in")
	fs.Var(&protectBackups, "protect-backup", "OdooBackup never forgotten (repeatable)")
	fs.Var(&protectSnapshots, "protect-snapshot", "snapshot ID never forgotten (repeatable)")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	if withRetention {
		loc, err := schedule.Location(timeZone)
		if err != nil {
			fmt.Fprintln(os.Stderr, "backup: --time-zone:", err)
			return result.ExitUsage
		}
		ret.Policy.Location = loc
		if err := ret.Policy.Validate(); err != nil {
			fmt.Fprintln(os.Stderr, "backup:", err)
			return result.ExitUsage
		}
		ret.ProtectBackups, ret.ProtectSnapshots = protectBackups, protectSnapshots
		o.Retention = &ret
	}
	if o.Namespace == "" || o.Deployment == "" || o.Backup == "" {
		fmt.Fprintln(os.Stderr, "backup: --namespace, --deployment and --backup are required")
		return result.ExitUsage
	}
	o.Version = version
	e, err := c.env()
	if err != nil {
		return finish(c.terminationLog, result.Failed(result.StepConnect, err))
	}
	if o.Copies, err = agent.CopyTargetsFromEnv(e.Restic, copies); err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return result.ExitUsage
	}
	return finish(c.terminationLog, agent.Backup(ctx, e, o))
}

func runRestore(ctx context.Context, fs *flag.FlagSet, c *common, command string, args []string) int {
	c.register(fs)
	o := agent.RestoreOptions{Mode: agent.ModeRestore}
	if command == "undo" {
		o.Mode = agent.ModeUndo
	}
	var modules string
	var upgrade string
	var tags stringList
	fs.StringVar(&o.Suffix, "suffix", "", "this restore's suffix")
	fs.StringVar(&o.Snapshot, "snapshot", "", "snapshot to restore (restore): an ID, or latest")
	fs.StringVar(&o.Host, "host", "", "with --snapshot latest: the snapshots' host")
	fs.Var(&tags, "tag", "with --snapshot latest: a tag the snapshot carries (repeatable)")
	fs.StringVar(&o.File, "file", "", "Odoo backup file (.zip or .dump) to restore instead of a snapshot, "+
		"below --store-target; its last segment may be a pattern")
	fs.StringVar(&o.StoreTarget, "store-target", "", "the store's root, as rclone names it")
	fs.StringVar(&o.WorkDir, "work-dir", "", "folder the steps of a restore Job with an Odoo step share")
	fs.BoolVar(&o.LoadOnly, "load-only", false, "load and check the backup, then stop (the first step); "+
		"the result goes to <work-dir>/"+agent.WorkLoadResult)
	fs.BoolVar(&o.Neutralize, "neutralize", false, "require the Odoo step to have neutralized the backup")
	fs.StringVar(&upgrade, "upgrade", "", "comma-separated modules the Odoo step must have upgraded")
	fs.StringVar(&o.UpgradeImage, "upgrade-image", "", "with --upgrade: the image the Odoo step upgraded on")
	fs.StringVar(&o.UndoSuffix, "undo-suffix", "", "suffix of the restore to undo (undo)")
	fs.BoolVar(&o.FinishSwap, "finish-swap", false, "only finish a swap that has started, from the restore journal; "+
		"needs no source, and fails changing nothing when the swap has not started")
	fs.StringVar(&o.DataRoot, "data", layout.DataMount, "root of the instance's data volume")
	fs.StringVar(&modules, "expect-modules", "", "comma-separated modules the instance should have")
	fs.IntVar(&o.CheckSample, "check-sample", 200, "restored files whose content is checked")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	for _, m := range strings.Split(modules, ",") {
		if m = strings.TrimSpace(m); m != "" {
			o.ExpectModules = append(o.ExpectModules, m)
		}
	}
	for _, m := range strings.Split(upgrade, ",") {
		if m = strings.TrimSpace(m); m != "" {
			o.UpgradeModules = append(o.UpgradeModules, m)
		}
	}
	if len(o.UpgradeModules) > 0 && (o.UpgradeImage == "" || o.WorkDir == "") {
		fmt.Fprintln(os.Stderr, "restore: --upgrade needs --upgrade-image and --work-dir")
		return result.ExitUsage
	}
	o.Tags = tags
	if o.File != "" && (!agent.IsBackupFile(o.File) || o.StoreTarget == "") {
		fmt.Fprintln(os.Stderr, "restore: --file must end in .zip or .dump, and needs --store-target")
		return result.ExitUsage
	}
	if o.LoadOnly && o.WorkDir == "" {
		fmt.Fprintln(os.Stderr, "restore: --load-only needs --work-dir")
		return result.ExitUsage
	}
	if o.FinishSwap && (o.LoadOnly || o.WorkDir != "") {
		fmt.Fprintln(os.Stderr, "restore: --finish-swap runs alone, without --load-only or --work-dir")
		return result.ExitUsage
	}
	var res result.Result
	if e, err := c.env(); err != nil {
		res = result.Failed(result.StepConnect, err)
	} else {
		res = agent.Restore(ctx, e, o)
	}
	if o.LoadOnly {
		// The last step reports; this one always lets the next steps run.
		if err := result.Write(filepath.Join(o.WorkDir, agent.WorkLoadResult), res); err != nil {
			fmt.Fprintln(os.Stderr, "could not write the load result:", err)
			return 1
		}
		if !res.OK {
			fmt.Fprintf(os.Stderr, "load failed at step %s: %s\n", res.Step, res.Error)
		}
		return 0
	}
	return finish(c.terminationLog, res)
}

func runDrill(ctx context.Context, fs *flag.FlagSet, c *common, command string, args []string) int {
	c.register(fs)
	var o agent.DrillOptions
	var modules string
	fs.StringVar(&o.Suffix, "suffix", "", "this drill's suffix")
	fs.StringVar(&o.Snapshot, "snapshot", "", "snapshot to restore (drill-load)")
	fs.StringVar(&o.Root, "root", layout.DrillRoot, "scratch folder shared by the drill's steps")
	fs.StringVar(&modules, "expect-modules", "", "comma-separated modules the instance should have")
	fs.IntVar(&o.CheckSample, "check-sample", 200, "restored files whose content is checked")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	for _, m := range strings.Split(modules, ",") {
		if m = strings.TrimSpace(m); m != "" {
			o.ExpectModules = append(o.ExpectModules, m)
		}
	}
	e, err := c.env()
	if command == "drill-finish" {
		if err != nil {
			return finish(c.terminationLog, result.Failed(result.StepConnect, err))
		}
		return finish(c.terminationLog, agent.DrillFinish(ctx, e, o))
	}
	if err == nil {
		err = agent.DrillLoad(ctx, e, o)
	}
	if err != nil {
		// Without a report the last step says the load did not finish.
		fmt.Fprintln(os.Stderr, "drill-load:", err)
	}
	return 0
}

func runStoreTask(ctx context.Context, fs *flag.FlagSet, task string, args []string) int {
	var repos, options stringList
	readData := fs.String("read-data", "", "check: part of the data to read back (3/7, or all)")
	resticBin := fs.String("restic", "restic", "restic executable")
	retryLock := fs.Duration("retry-lock", 2*time.Hour, "how long restic waits for a repository lock")
	terminationLog := fs.String("termination-log", layout.TerminationLog, "where the result is written")
	fs.Var(&repos, "repository", "<namespace>/<deployment>=<restic repository> (repeatable)")
	fs.Var(&options, "restic-option", "restic -o option (repeatable)")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	o := agent.StoreTaskOptions{Task: task, ReadData: *readData}
	for _, r := range repos {
		name, url, ok := strings.Cut(r, "=")
		if !ok || name == "" || url == "" {
			fmt.Fprintf(os.Stderr, "%s: --repository %q is not <name>=<repository>\n", task, r)
			return result.ExitUsage
		}
		o.Repositories = append(o.Repositories, agent.Repository{Name: name, URL: url})
	}
	base := &restic.Restic{Bin: *resticBin, Options: options, RetryLock: *retryLock, Log: os.Stderr}
	return finish(*terminationLog, agent.StoreTask(ctx, base, agent.NewLogger(os.Stderr), o))
}

func runCleanup(ctx context.Context, fs *flag.FlagSet, c *common, args []string) int {
	c.register(fs)
	suffix := fs.String("suffix", "", "suffix of the restore whose data is deleted")
	dataRoot := fs.String("data", layout.DataMount, "root of the instance's data volume")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	e, err := c.env()
	if err != nil {
		return finish(c.terminationLog, result.Failed(result.StepConnect, err))
	}
	return finish(c.terminationLog, agent.Cleanup(ctx, e, *dataRoot, *suffix))
}

// runExport writes a snapshot as an Odoo zip. It needs only the repository
// (RESTIC_REPOSITORY and its password, as restic reads them), not odoo.conf.
func runExport(ctx context.Context, fs *flag.FlagSet, c *common, args []string) int {
	c.register(fs)
	_ = fs.Lookup("config").Value.Set("")
	var o agent.ExportOptions
	var tags stringList
	fs.StringVar(&o.Snapshot, "snapshot", "latest", "snapshot to export: an ID, or latest")
	fs.StringVar(&o.Host, "host", "", "with --snapshot latest: the snapshots' host (<namespace>/<name>)")
	fs.Var(&tags, "tag", "with --snapshot latest: a tag the snapshot has (repeatable)")
	fs.StringVar(&o.Output, "output", "", "file to write the zip to; - for stdout")
	fs.StringVar(&o.Target, "target", "", "rclone path to upload the zip to; ending in / names the zip <db>_<time>.zip")
	if err := fs.Parse(args); err != nil {
		return result.ExitUsage
	}
	o.Tags = tags
	if (o.Output == "") == (o.Target == "") {
		fmt.Fprintln(os.Stderr, "export: set one of --output and --target")
		return result.ExitUsage
	}
	o.Stdout = os.Stdout
	e, err := c.env()
	if err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return result.ExitUsage
	}
	r := agent.Export(ctx, e, o)
	if r.OK {
		fmt.Fprintf(os.Stderr, "exported snapshot %s to %s\n", r.SnapshotID, r.File)
	}
	return finish(c.terminationLog, r)
}

// finish writes the result and returns the exit code (result.ExitCode): 0
// when it succeeded (or was deferred), result.ExitFailed when it failed, so
// the Job's backoff retries it, and result.ExitFinal when no retry can change
// the outcome, which the Job's pod failure policy ends at once.
func finish(terminationLog string, r result.Result) int {
	if err := result.Write(terminationLog, r); err != nil {
		fmt.Fprintln(os.Stderr, "could not write the result:", err)
	}
	if r.OK {
		fmt.Fprintln(os.Stderr, "done")
		return 0
	}
	fmt.Fprintf(os.Stderr, "failed at step %s: %s\n", r.Step, r.Error)
	if r.Final {
		fmt.Fprintln(os.Stderr, "a retry cannot change this: the Job ends without its other attempts")
	}
	return result.ExitCode(r)
}
