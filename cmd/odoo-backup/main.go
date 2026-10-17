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
	terminationLog string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", layout.ConfigMount+"/odoo.conf", "odoo.conf of the instance")
	fs.StringVar(&c.maintenanceDB, "maintenance-db", "postgres", "database to run CREATE/DROP/RENAME DATABASE from")
	fs.StringVar(&c.pgBinTemplate, "pg-bin-template", os.Getenv("ODOO_BACKUP_PG_BIN_TEMPLATE"),
		"folder of each PostgreSQL client major, with %d for the major; empty uses PATH")
	fs.StringVar(&c.pgMajors, "pg-majors", envOr("ODOO_BACKUP_PG_MAJORS", "13,14,15,16,17,18"), "client majors to look for")
	fs.StringVar(&c.resticBin, "restic", "restic", "restic executable")
	fs.Var(&c.resticOptions, "restic-option", "restic -o option (repeatable)")
	fs.DurationVar(&c.retryLock, "retry-lock", 31*time.Minute, "how long restic waits for a repository lock")
	fs.StringVar(&c.terminationLog, "termination-log", layout.TerminationLog, "where the result is written")
}

func (c *common) env() (*agent.Env, error) {
	cfg, err := odooconf.Load(c.config)
	if err != nil {
		return nil, err
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
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Args[1], os.Args[2:]))
}

func run(ctx context.Context, command string, args []string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	var c common
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
	case "prepare":
		dataRoot := fs.String("data", layout.DataMount, "root of the instance's data volume")
		database := fs.String("database", "", "database name (the filestore folder)")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if *database == "" {
			fmt.Fprintln(os.Stderr, "prepare: --database is required")
			return 2
		}
		if err := agent.Prepare(*dataRoot, *database); err != nil {
			fmt.Fprintln(os.Stderr, "prepare:", err)
			return 1
		}
		return 0
	case "probe":
		target := fs.String("target", "", "the store root, as rclone names it")
		terminationLog := fs.String("termination-log", layout.TerminationLog, "where the result is written")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		return finish(*terminationLog, agent.Probe(ctx, agent.ProbeOptions{Target: *target}))
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.Namespace == "" || o.Deployment == "" || o.Backup == "" {
		fmt.Fprintln(os.Stderr, "backup: --namespace, --deployment and --backup are required")
		return 2
	}
	o.Version = version
	e, err := c.env()
	if err != nil {
		return finish(c.terminationLog, result.Failed(result.StepConnect, err))
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
	fs.StringVar(&o.Suffix, "suffix", "", "this restore's suffix")
	fs.StringVar(&o.Snapshot, "snapshot", "", "snapshot to restore (restore)")
	fs.StringVar(&o.UndoSuffix, "undo-suffix", "", "suffix of the restore to undo (undo)")
	fs.StringVar(&o.DataRoot, "data", layout.DataMount, "root of the instance's data volume")
	fs.StringVar(&modules, "expect-modules", "", "comma-separated modules the instance should have")
	fs.IntVar(&o.CheckSample, "check-sample", 200, "restored files whose content is checked")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for _, m := range strings.Split(modules, ",") {
		if m = strings.TrimSpace(m); m != "" {
			o.ExpectModules = append(o.ExpectModules, m)
		}
	}
	e, err := c.env()
	if err != nil {
		return finish(c.terminationLog, result.Failed(result.StepConnect, err))
	}
	return finish(c.terminationLog, agent.Restore(ctx, e, o))
}

func runCleanup(ctx context.Context, fs *flag.FlagSet, c *common, args []string) int {
	c.register(fs)
	suffix := fs.String("suffix", "", "suffix of the restore whose data is deleted")
	dataRoot := fs.String("data", layout.DataMount, "root of the instance's data volume")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e, err := c.env()
	if err != nil {
		return finish(c.terminationLog, result.Failed(result.StepConnect, err))
	}
	return finish(c.terminationLog, agent.Cleanup(ctx, e, *dataRoot, *suffix))
}

// finish writes the result and returns the exit code: 0 when it succeeded
// (or was deferred), 1 when it failed, so the Job's backoff retries it.
func finish(terminationLog string, r result.Result) int {
	if err := result.Write(terminationLog, r); err != nil {
		fmt.Fprintln(os.Stderr, "could not write the result:", err)
	}
	if r.OK {
		fmt.Fprintln(os.Stderr, "done")
		return 0
	}
	fmt.Fprintf(os.Stderr, "failed at step %s: %s\n", r.Step, r.Error)
	return 1
}
