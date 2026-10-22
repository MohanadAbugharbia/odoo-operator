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

// Package result is what a backup, restore, cleanup or probe Job reports
// back to the operator. The Job writes it as JSON to its termination message
// (the Job pods have no Kubernetes credentials), and the operator reads it
// from the pod's terminated state. Kubernetes keeps at most 4096 bytes of a
// termination message, so Encode trims what only informs until it fits, and
// never silently drops what the operator acts on (see Encode).
package result

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MaxBytes is the most Kubernetes keeps of a termination message.
const MaxBytes = 4096

// Exit codes of the agent's commands. A Job's pod failure policy ends the
// Job at once on ExitUsage and ExitFinal, which a retry cannot change; any
// other failure (ExitFailed, a killed or evicted pod) is retried up to the
// Job's backoff limit.
const (
	// ExitFailed: the Job failed in a way a retry may get past.
	ExitFailed = 1
	// ExitUsage: the command line is wrong (flags the agent does not know).
	ExitUsage = 2
	// ExitFinal: the Job failed in a way no retry can change (Result.Final),
	// such as a backup that does not pass its checks.
	ExitFinal = 3
)

// Steps a Job reports failing at.
const (
	StepConnect   = "connect"
	StepWait      = "wait"
	StepRepo      = "repository"
	StepDump      = "dump"
	StepFilestore = "filestore"
	StepUpload    = "upload"
	StepVerify    = "verify"
	StepFetch     = "fetch"
	StepLoad      = "load"
	StepCheck     = "check"
	StepSwap      = "swap"
	StepCleanup   = "cleanup"
	StepProbe     = "probe"
	StepStoreTask = "store-task"
	StepOdoo      = "odoo"
)

// Result is the outcome of one Job.
type Result struct {
	// OK is true when the Job did what it was asked.
	OK bool `json:"ok"`
	// Step is where a failed Job stopped.
	Step string `json:"step,omitempty"`
	// Error says why it failed.
	Error string `json:"error,omitempty"`
	// Final: the failure is one a retry of the Job cannot change, such as a
	// restore refused by its checks. The agent exits with ExitFinal, which
	// ends the Job without its other attempts.
	Final bool `json:"final,omitempty"`
	// Deferred: the backup did not start because Odoo's filestore cleanup
	// is running or about to; the operator retries.
	Deferred bool `json:"deferred,omitempty"`
	// Warnings that did not fail the Job.
	Warnings []string `json:"warnings,omitempty"`
	// File is the Odoo backup file a restore read, when it was not a
	// snapshot.
	File string `json:"file,omitempty"`

	// Backup.
	SnapshotID      string   `json:"snapshotID,omitempty"`
	OdooVersion     string   `json:"odooVersion,omitempty"`
	PgServerVersion string   `json:"pgServerVersion,omitempty"`
	DatabaseBytes   int64    `json:"databaseBytes,omitempty"`
	FilestoreBytes  int64    `json:"filestoreBytes,omitempty"`
	FilestoreFiles  int64    `json:"filestoreFiles,omitempty"`
	AddedBytes      int64    `json:"addedBytes,omitempty"`
	RepairedFiles   int      `json:"repairedFiles,omitempty"`
	MissingFiles    int      `json:"missingFiles,omitempty"`
	MissingNames    []string `json:"missingNames,omitempty"`
	// Forgotten are the IDs of the snapshots retention forgot in the
	// instance's repository (at most retention.MaxForget, so the list is
	// never trimmed).
	Forgotten []string `json:"forgotten,omitempty"`
	// RetentionError says why retention did not run; the backup itself
	// succeeded.
	RetentionError string `json:"retentionError,omitempty"`
	// Copies are the copies of the new snapshot in the other stores.
	Copies []Copy `json:"copies,omitempty"`

	// Store check or prune.
	// Repositories is how many instance repositories the task went
	// through; StoredBytes how much they hold together.
	Repositories int   `json:"repositories,omitempty"`
	StoredBytes  int64 `json:"storedBytes,omitempty"`
	// RepositoryErrors are the repositories that failed, as "<repo>: <error>".
	RepositoryErrors []string `json:"repositoryErrors,omitempty"`

	// Restore.
	// Image and UpgradeToken are what the backup's instance ran.
	Image        string `json:"image,omitempty"`
	UpgradeToken string `json:"upgradeToken,omitempty"`
	// MissingModules are modules the operator expected (spec.modules) that
	// the restored database does not have installed. The operator installs
	// them, so Encode never shortens the list: when it does not fit, it
	// drops it whole and sets MissingModulesUnknown.
	MissingModules []string `json:"missingModules,omitempty"`
	// MissingModulesUnknown is set by Encode when MissingModules did not fit
	// in the termination message: which modules the restored database lacks
	// is then not in the result, and the operator reads the installed
	// modules from the database itself.
	MissingModulesUnknown bool `json:"missingModulesUnknown,omitempty"`
	// LiveChanged is true once the live database or filestore may have been
	// renamed: from then on the restore is only ever finished, never
	// abandoned.
	LiveChanged bool `json:"liveChanged,omitempty"`
	// PreviousKept is true when the replaced database or filestore was kept
	// under its _prev name.
	PreviousKept bool `json:"previousKept,omitempty"`
}

// Copy is the copy of a backup in one other store.
type Copy struct {
	Store      string `json:"store"`
	SnapshotID string `json:"snapshotID,omitempty"`
	// File is the zip a store of format zip got, below its root.
	File  string `json:"file,omitempty"`
	Error string `json:"error,omitempty"`
}

// Failed returns a failed Result.
func Failed(step string, err error) Result {
	return Result{Step: step, Error: err.Error()}
}

// ExitCode is the exit code of a command that ends with r: 0 when it
// succeeded (or was deferred), ExitFinal for a final failure, ExitFailed
// for any other.
func ExitCode(r Result) int {
	switch {
	case r.OK:
		return 0
	case r.Final:
		return ExitFinal
	}
	return ExitFailed
}

const (
	moreWarnings = "(more warnings in the Job log)"
	errorCut     = "… (see the Job log)"
)

// Encode returns r as JSON no longer than MaxBytes. It first drops list
// entries and shortens the error, which only inform. If that is not enough it
// drops the warnings, then MissingModules as a whole (setting
// MissingModulesUnknown, never keeping part of the list), then shortens the
// longest free-form field in turn. The flags (ok, final, deferred,
// liveChanged, previousKept) always survive.
func Encode(r Result) []byte {
	for {
		b, err := json.Marshal(r)
		if err != nil {
			// Only strings, numbers and slices: cannot happen.
			return []byte(`{"ok":false,"error":"could not encode the result"}`)
		}
		if len(b) <= MaxBytes {
			return b
		}
		// Every case must make r strictly smaller or stop matching, or this
		// loops forever.
		switch {
		case len(r.MissingNames) > 0:
			r.MissingNames = r.MissingNames[:len(r.MissingNames)/2]
		case len(r.RepositoryErrors) > 1:
			r.RepositoryErrors = r.RepositoryErrors[:len(r.RepositoryErrors)/2]
		case len(r.RepositoryErrors) == 1 && len(r.RepositoryErrors[0]) > 512:
			r.RepositoryErrors = []string{r.RepositoryErrors[0][:512-len(errorCut)] + errorCut}
		case len(r.Warnings) > 2 || (len(r.Warnings) == 2 && r.Warnings[1] != moreWarnings):
			r.Warnings = append(r.Warnings[:len(r.Warnings)/2:len(r.Warnings)/2], moreWarnings)
		case len(r.Error) > 512:
			r.Error = r.Error[:512-len(errorCut)] + errorCut
		case len(r.RetentionError) > 512:
			r.RetentionError = r.RetentionError[:512-len(errorCut)] + errorCut
		case copyErrorsOver(r.Copies, 256):
			cs := append([]Copy(nil), r.Copies...)
			for i := range cs {
				if len(cs[i].Error) > 256 {
					cs[i].Error = cs[i].Error[:256-len(errorCut)] + errorCut
				}
			}
			r.Copies = cs
		case len(r.Warnings) > 0 && len(r.Warnings[0]) > 512:
			// A new slice: r shares its backing array with the caller's.
			r.Warnings = append([]string{r.Warnings[0][:512-len("…")] + "…"}, r.Warnings[1:]...)
		default:
			return encodeEssential(r)
		}
	}
}

// encodeEssential encodes a result that the list trims of Encode could not
// make fit.
func encodeEssential(r Result) []byte {
	r.Warnings, r.MissingNames = nil, nil
	if b, ok := fits(r); ok {
		return b
	}
	if len(r.MissingModules) > 0 {
		r.MissingModules, r.MissingModulesUnknown = nil, true
		if b, ok := fits(r); ok {
			return b
		}
	}
	r.Copies, r.RepositoryErrors = shortCopies(r.Copies), nil
	for {
		if b, ok := fits(r); ok {
			return b
		}
		// Halve the longest free-form field; each one is bounded only by
		// what the Job was given, and every byte may be JSON-escaped.
		fields := []*string{&r.Step, &r.Error, &r.SnapshotID, &r.OdooVersion, &r.PgServerVersion,
			&r.Image, &r.UpgradeToken, &r.File, &r.RetentionError}
		longest := fields[0]
		for _, f := range fields[1:] {
			if len(*f) > len(*longest) {
				longest = f
			}
		}
		switch {
		case len(*longest) > 16:
			*longest = truncate(*longest, len(*longest)/2)
		case len(r.Copies) > 0:
			r.Copies = nil
		case len(r.Forgotten) > 0:
			r.Forgotten = nil
		default:
			// Cannot happen: what is left is a few short strings and numbers.
			return []byte(fmt.Sprintf(`{"ok":false,"liveChanged":%t,"error":"could not encode the result"}`, r.LiveChanged))
		}
	}
}

func fits(r Result) ([]byte, bool) {
	b, err := json.Marshal(r)
	return b, err == nil && len(b) <= MaxBytes
}

// Write writes r to path (the container's termination message file).
func Write(path string, r Result) error {
	return os.WriteFile(path, Encode(r), 0o644)
}

func copyErrorsOver(cs []Copy, n int) bool {
	for _, c := range cs {
		if len(c.Error) > n {
			return true
		}
	}
	return false
}

// shortCopies keeps whether each copy worked, with a short error.
func shortCopies(cs []Copy) []Copy {
	out := make([]Copy, 0, len(cs))
	for _, c := range cs {
		out = append(out, Copy{Store: truncate(c.Store, 64), SnapshotID: truncate(c.SnapshotID, 64),
			File: truncate(c.File, 256), Error: truncate(c.Error, 64)})
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ErrNoResult is returned by Decode for an empty message.
var ErrNoResult = errors.New("the Job left no result")

// Decode parses a termination message.
func Decode(message string) (Result, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return Result{}, ErrNoResult
	}
	var r Result
	if err := json.Unmarshal([]byte(message), &r); err != nil {
		return Result{}, fmt.Errorf("the Job's result is not readable: %w", err)
	}
	return r, nil
}
