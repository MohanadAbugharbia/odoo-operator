package maintenancepage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// Page states. The page renders one screen per state.
const (
	// StatePreparing: the database is being prepared for the first setup.
	StatePreparing = "preparing"
	// StateInstalling: the init Job installs the modules.
	StateInstalling = "installing"
	// StateUpdating: the upgrade Job updates the database.
	StateUpdating = "updating"
	// StateStarting: no Job is running, Odoo is starting.
	StateStarting = "starting"
	// StateFailed: the init or upgrade Job failed.
	StateFailed = "failed"
	// StateUnavailable: no status has been read yet.
	StateUnavailable = "unavailable"
)

// Status is what /__maintenance/status.json answers: a trimmed reading of
// the OdooDeployment status. It carries no failure details, no secrets and
// nothing about the database.
type Status struct {
	State string `json:"state"`
	// Job is the kind of the maintenance Job, "init" or "upgrade".
	Job string `json:"job,omitempty"`
	// StartedAt is when the running Job was created.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// ExpectedSeconds is how long the last upgrade took (updating only).
	ExpectedSeconds int64 `json:"expectedSeconds,omitempty"`
	// Version is the tag of the image being installed (updating only).
	Version string `json:"version,omitempty"`
	// Stale is set when the status could not be read just now and the state
	// is the last one known.
	Stale bool `json:"stale,omitempty"`
	// Now is the server's clock, so the page can count elapsed time from it.
	Now time.Time `json:"now"`
}

// StatusOf maps an OdooDeployment onto the page state. Now is left to the
// server.
func StatusOf(od *odoov1.OdooDeployment) Status {
	st := Status{}
	job := od.Status.CurrentInitJob
	st.Job = job.Kind
	if job.Name != "" && job.StartedAt != nil {
		t := job.StartedAt.UTC()
		st.StartedAt = &t
	}
	switch od.Status.Phase {
	case odoov1.PhaseFailed:
		st.State = StateFailed
		if st.Job == "" && len(od.Status.InitModulesInstalled) == 0 {
			st.Job = odoov1.JobKindInit
		}
		st.StartedAt = nil
	case odoov1.PhaseInitializing:
		st.State = StateInstalling
		st.Job = odoov1.JobKindInit
	case odoov1.PhaseUpgrading:
		st.State = StateUpdating
		st.Job = odoov1.JobKindUpgrade
		st.Version = imageTag(job.Image)
		if d := od.Status.LastMaintenanceDuration; d != nil {
			st.ExpectedSeconds = int64(d.Seconds())
		}
	case odoov1.PhaseRunning:
		st.State = StateStarting
		st.StartedAt = nil
	default: // Pending
		if len(od.Status.InitModulesInstalled) == 0 {
			st.State = StatePreparing
		} else {
			st.State = StateStarting
		}
		st.StartedAt = nil
	}
	return st
}

// imageTag returns the tag of an image reference ("" for a digest or none).
func imageTag(image string) string {
	if image == "" || strings.Contains(image, "@") {
		return ""
	}
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[colon+1:]
	}
	return ""
}

// Snapshot is what the operator writes into the <name>-maintenance ConfigMap
// and the server reads from its mount: the branding and the page state.
type Snapshot struct {
	Page   odoov1.MaintenancePageConfig `json:"page"`
	Status Status                       `json:"status"`
}

// SnapshotOf is the Snapshot of an OdooDeployment. It holds no clock, so it
// only changes when the page has something new to show.
func SnapshotOf(od *odoov1.OdooDeployment) Snapshot {
	return Snapshot{Page: od.Spec.MaintenancePage, Status: StatusOf(od)}
}

// Source returns the current Snapshot.
type Source interface {
	// Get returns the Snapshot, or the last one read with the error that
	// kept it from being refreshed (ok is false when none was ever read).
	Get() (snap Snapshot, ok bool, err error)
}

// fileSource reads the Snapshot from the mounted ConfigMap key, keeping the
// last good one when a read fails (the kubelet swaps the file atomically, so
// that should not happen).
type fileSource struct {
	path string

	mu   sync.Mutex
	last Snapshot
	ok   bool
}

// NewFileSource returns a Source reading path.
func NewFileSource(path string) Source {
	return &fileSource{path: path}
}

func (s *fileSource) Get() (Snapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s.last, s.ok, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return s.last, s.ok, fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.last, s.ok = snap, true
	return snap, true, nil
}
