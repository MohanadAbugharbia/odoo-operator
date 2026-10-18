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

// Package restic runs the restic command line. The repository, password and
// credentials come from the environment (RESTIC_REPOSITORY,
// RESTIC_PASSWORD_FILE, AWS_*, RCLONE_*), exactly as a person running restic
// by hand would set them, so every backup the operator writes can be read
// with plain restic.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Exit codes restic documents.
// jsonFlag asks restic for machine-readable output.
const jsonFlag = "--json"

const (
	exitIncomplete   = 3
	exitNoRepository = 10
	exitLocked       = 11
	exitWrongKey     = 12
)

// ErrWrongPassword is returned when the password does not open the repository.
var ErrWrongPassword = errors.New("the encryption key does not open the repository (wrong password)")

// ErrLocked is returned when the repository stayed locked.
var ErrLocked = errors.New("the repository is locked by another restic process")

// Error is a failed restic command.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 2000 {
		msg = msg[len(msg)-2000:]
	}
	return fmt.Sprintf("restic %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, msg)
}

// Restic runs restic commands against one repository.
type Restic struct {
	// Bin is the restic executable; "restic" when empty.
	Bin string
	// Env is added to the process environment.
	Env []string
	// Options are passed as -o options (such as s3.bucket-lookup=path).
	Options []string
	// RetryLock is how long a command waits for a lock.
	RetryLock time.Duration
	// Log receives restic's progress output; discarded when nil.
	Log io.Writer
}

func (r *Restic) bin() string {
	if r.Bin == "" {
		return "restic"
	}
	return r.Bin
}

func (r *Restic) command(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2*len(r.Options)+2)
	for _, o := range r.Options {
		full = append(full, "-o", o)
	}
	if r.RetryLock > 0 {
		full = append(full, "--retry-lock", r.RetryLock.String())
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.bin(), full...)
	cmd.Env = append(os.Environ(), r.Env...)
	cmd.WaitDelay = 30 * time.Second
	return cmd
}

func (r *Restic) logWriter() io.Writer {
	if r.Log == nil {
		return io.Discard
	}
	return r.Log
}

// run runs a command and returns its stdout.
func (r *Restic) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(&stderr, r.logWriter())
	err := cmd.Run()
	return stdout.Bytes(), r.wrap(args, err, stderr.String())
}

func (r *Restic) wrap(args []string, err error, stderr string) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("restic %s: %w", strings.Join(args, " "), err)
	}
	e := &Error{Args: args, ExitCode: exitErr.ExitCode(), Stderr: stderr}
	switch e.ExitCode {
	case exitWrongKey:
		return fmt.Errorf("%w: %s", ErrWrongPassword, e.Error())
	case exitLocked:
		return fmt.Errorf("%w: %s", ErrLocked, e.Error())
	}
	return e
}

// Exists reports whether the repository exists.
func (r *Restic) Exists(ctx context.Context) (bool, error) {
	_, err := r.run(ctx, "cat", "config")
	var e *Error
	if errors.As(err, &e) && e.ExitCode == exitNoRepository {
		return false, nil
	}
	return err == nil, err
}

// Init creates the repository.
func (r *Restic) Init(ctx context.Context) error {
	_, err := r.run(ctx, "init")
	return err
}

// EnsureRepository creates the repository unless it exists.
func (r *Restic) EnsureRepository(ctx context.Context) (created bool, err error) {
	exists, err := r.Exists(ctx)
	if err != nil || exists {
		return false, err
	}
	if err := r.Init(ctx); err != nil {
		// Two Jobs never back up one instance at once, but a person may have
		// run init meanwhile.
		if exists, existsErr := r.Exists(ctx); existsErr == nil && exists {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Unlock removes stale locks (locks of processes that are gone).
func (r *Restic) Unlock(ctx context.Context) error {
	_, err := r.run(ctx, "unlock")
	return err
}

// Summary is the summary restic backup prints.
type Summary struct {
	SnapshotID          string `json:"snapshot_id"`
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	FilesUnmodified     int64  `json:"files_unmodified"`
	DataAdded           int64  `json:"data_added"`
	DataAddedPacked     int64  `json:"data_added_packed"`
	TotalFilesProcessed int64  `json:"total_files_processed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
}

// BackupResult is what restic backup did.
type BackupResult struct {
	Summary Summary
	// Unreadable lists files restic could not read; the snapshot was
	// written without them.
	Unreadable []string
}

// BackupOptions are the flags of a backup.
type BackupOptions struct {
	Host     string
	Tags     []string
	Excludes []string
	Time     time.Time
}

// Backup backs up paths into a new snapshot.
func (r *Restic) Backup(ctx context.Context, opts BackupOptions, paths ...string) (BackupResult, error) {
	args := []string{"backup", jsonFlag}
	if opts.Host != "" {
		args = append(args, "--host", opts.Host)
	}
	for _, t := range opts.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range opts.Excludes {
		args = append(args, "--exclude", e)
	}
	if !opts.Time.IsZero() {
		args = append(args, "--time", opts.Time.UTC().Format("2006-01-02 15:04:05"))
	}
	args = append(args, paths...)
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(&stderr, r.logWriter())
	runErr := r.wrap(args, cmd.Run(), stderr.String())

	var res BackupResult
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg struct {
			MessageType string `json:"message_type"`
			Item        string `json:"item"`
			Summary
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch msg.MessageType {
		case "summary":
			res.Summary = msg.Summary
		case "error":
			if msg.Item != "" {
				res.Unreadable = append(res.Unreadable, msg.Item)
			}
		}
	}
	var e *Error
	if errors.As(runErr, &e) && e.ExitCode == exitIncomplete && res.Summary.SnapshotID != "" {
		// The snapshot exists without the unreadable files; the caller
		// decides what they mean.
		return res, nil
	}
	if runErr != nil {
		return res, runErr
	}
	if res.Summary.SnapshotID == "" {
		return res, fmt.Errorf("restic backup printed no snapshot id")
	}
	return res, nil
}

// Snapshot is one entry of restic snapshots.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Paths    []string  `json:"paths"`
	// Original is the snapshot this one was copied from, if it is a copy.
	Original string `json:"original,omitempty"`
}

// HasTag reports whether the snapshot carries tag.
func (s Snapshot) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Snapshots lists the snapshots of host carrying every tag in tags, oldest first.
func (r *Restic) Snapshots(ctx context.Context, host string, tags ...string) ([]Snapshot, error) {
	args := []string{"snapshots", jsonFlag}
	if host != "" {
		args = append(args, "--host", host)
	}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	return snaps, nil
}

// Snapshot returns one snapshot by (short) id.
func (r *Restic) Snapshot(ctx context.Context, id string) (Snapshot, error) {
	out, err := r.run(ctx, "snapshots", jsonFlag, id)
	if err != nil {
		return Snapshot{}, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return Snapshot{}, fmt.Errorf("restic snapshots: %w", err)
	}
	if len(snaps) != 1 {
		return Snapshot{}, fmt.Errorf("snapshot %s not found in the repository", id)
	}
	return snaps[0], nil
}

// lookup returns the value of an environment variable as the restic
// process sees it: r.Env, the later entry winning, over the process's own.
func (r *Restic) lookup(key string) string {
	for i := len(r.Env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(r.Env[i], "="); ok && k == key {
			return v
		}
	}
	return os.Getenv(key)
}

// fromEnv points restic's --from-repo at src's repository.
func (r *Restic) fromEnv(src *Restic) []string {
	env := append([]string(nil), r.Env...)
	for _, k := range []string{"REPOSITORY", "PASSWORD_FILE", "PASSWORD"} {
		if v := src.lookup("RESTIC_" + k); v != "" {
			env = append(env, "RESTIC_FROM_"+k+"="+v)
		}
	}
	return env
}

// EnsureCopyOf creates the repository, unless it exists, with src's chunker
// parameters, so that snapshots copied from src stay deduplicated.
func (r *Restic) EnsureCopyOf(ctx context.Context, src *Restic) (created bool, err error) {
	exists, err := r.Exists(ctx)
	if err != nil || exists {
		return false, err
	}
	with := *r
	with.Env = r.fromEnv(src)
	if _, err := with.run(ctx, "init", "--copy-chunker-params"); err != nil {
		if exists, existsErr := r.Exists(ctx); existsErr == nil && exists {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CopyFrom copies snapshot id from src into this repository. A snapshot
// copied before is not copied again.
func (r *Restic) CopyFrom(ctx context.Context, src *Restic, id string) error {
	with := *r
	with.Env = r.fromEnv(src)
	_, err := with.run(ctx, "copy", id)
	return err
}

// Forget removes snapshots by ID. Their data stays in the repository until
// a prune.
func (r *Restic) Forget(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.run(ctx, append([]string{"forget"}, ids...)...)
	return err
}

// TagValue returns the value of the snapshot's tag with prefix (such as
// "kind="), or "".
func (s Snapshot) TagValue(prefix string) string {
	for _, t := range s.Tags {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return ""
}

// Node is one entry of restic ls.
type Node struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Ls calls fn for every node under dir in snapshot.
func (r *Restic) Ls(ctx context.Context, snapshot, dir string, fn func(Node)) error {
	args := []string{"ls", jsonFlag, "--recursive", snapshot, dir}
	cmd := r.command(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderr, r.logWriter())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restic ls: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var n struct {
			StructType  string `json:"struct_type"`
			MessageType string `json:"message_type"`
			Node
		}
		if json.Unmarshal(scanner.Bytes(), &n) != nil {
			continue
		}
		if n.StructType == "node" || n.MessageType == "node" {
			fn(n.Node)
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_, _ = io.Copy(io.Discard, stdout)
	}
	if err := r.wrap(args, cmd.Wait(), stderr.String()); err != nil {
		return err
	}
	return scanErr
}

// Dump writes file from snapshot to w.
func (r *Restic) Dump(ctx context.Context, snapshot, file string, w io.Writer) error {
	args := []string{"dump", snapshot, file}
	cmd := r.command(ctx, args...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderr, r.logWriter())
	return r.wrap(args, cmd.Run(), stderr.String())
}

// Restore restores the paths listed in includeFile (one per line) from
// snapshot under target, keeping their full paths.
func (r *Restic) Restore(ctx context.Context, snapshot, target string, includes ...string) error {
	args := []string{"restore", snapshot, "--target", target}
	var listFile string
	if len(includes) > 0 {
		f, err := os.CreateTemp("", "restic-include-*")
		if err != nil {
			return err
		}
		listFile = f.Name()
		defer func() { _ = os.Remove(listFile) }()
		for _, inc := range includes {
			if strings.ContainsAny(inc, "\n\r") {
				_ = f.Close()
				return fmt.Errorf("restore: path %q contains a line break", inc)
			}
			// restic trims each line of the file, so such a path cannot be matched.
			if strings.TrimSpace(inc) != inc {
				_ = f.Close()
				return fmt.Errorf("restore: path %q starts or ends with a space", inc)
			}
			if _, err := fmt.Fprintln(f, escapePattern(inc)); err != nil {
				_ = f.Close()
				return err
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
		args = append(args, "--include-file", listFile)
	}
	_, err := r.run(ctx, args...)
	return err
}

// escapePattern escapes the characters restic's include patterns treat
// specially, so a path matches only itself. In an include file restic also
// expands $VAR ("$$" is a literal "$"), skips lines starting with "#" and
// takes a leading "!" as a negation.
func escapePattern(p string) string {
	var b strings.Builder
	for i, c := range p {
		switch {
		case c == '*', c == '?', c == '[', c == ']', c == '\\', (c == '#' || c == '!') && i == 0:
			b.WriteRune('\\')
		case c == '$':
			b.WriteRune('$')
		}
		b.WriteRune(c)
	}
	return b.String()
}
