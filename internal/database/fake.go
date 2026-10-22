package database

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Fake is an in-memory Provisioner for tests. Databases are keyed by name and
// hold their owner comment; every call is recorded in Calls.
type Fake struct {
	mu sync.Mutex

	dbs   map[string]OwnerTag
	calls []string

	// Errors injected into the corresponding call when non-nil.
	ExistsErr  error
	CreateErr  error
	DropErr    error
	ModulesErr error

	// modules are the installed Odoo modules of each database (SetModules).
	modules map[string][]string
}

// SetModules records the Odoo modules installed in db, which
// InstalledModules returns.
func (f *Fake) SetModules(db string, modules ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modules == nil {
		f.modules = map[string][]string{}
	}
	f.modules[db] = append([]string(nil), modules...)
}

func (f *Fake) InstalledModules(_ context.Context, _ ConnInfo, db string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("InstalledModules", db)
	if f.ModulesErr != nil {
		return nil, f.ModulesErr
	}
	if _, ok := f.dbs[db]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, db)
	}
	return append([]string(nil), f.modules[db]...), nil
}

// Names returns the databases in the fake.
func (f *Fake) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.dbs))
	for db := range f.dbs {
		out = append(out, db)
	}
	return out
}

var _ Provisioner = (*Fake)(nil)

// NewFake returns an empty fake provisioner.
func NewFake() *Fake {
	return &Fake{dbs: map[string]OwnerTag{}}
}

// Seed registers a pre-existing database with the given comment ("" for none).
func (f *Fake) Seed(db string, tag OwnerTag) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbs[db] = tag
}

// Has reports whether the database exists in the fake.
func (f *Fake) Has(db string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.dbs[db]
	return ok
}

// TagOf returns the comment recorded for db.
func (f *Fake) TagOf(db string) OwnerTag {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dbs[db]
}

// Calls returns the recorded calls in order, formatted as "Op:db".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// CallCount returns how many times op ("Exists", "Create", "Tag", "OwnerTag",
// "Drop", "Databases", "DropCopy", "InstalledModules") was called.
func (f *Fake) CallCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) > len(op) && c[:len(op)+1] == op+":" {
			n++
		}
	}
	return n
}

func (f *Fake) record(op, db string) {
	f.calls = append(f.calls, fmt.Sprintf("%s:%s", op, db))
}

func (f *Fake) Exists(_ context.Context, _ ConnInfo, db string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Exists", db)
	if f.ExistsErr != nil {
		return false, f.ExistsErr
	}
	_, ok := f.dbs[db]
	return ok, nil
}

func (f *Fake) Create(_ context.Context, _ ConnInfo, db string, tag OwnerTag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Create", db)
	if f.CreateErr != nil {
		return f.CreateErr
	}
	if _, ok := f.dbs[db]; ok {
		return fmt.Errorf("database %q already exists", db)
	}
	f.dbs[db] = tag
	return nil
}

func (f *Fake) Tag(_ context.Context, _ ConnInfo, db string, tag OwnerTag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Tag", db)
	if _, ok := f.dbs[db]; !ok {
		return fmt.Errorf("database %q does not exist", db)
	}
	f.dbs[db] = tag
	return nil
}

func (f *Fake) OwnerTag(_ context.Context, _ ConnInfo, db string) (OwnerTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("OwnerTag", db)
	tag, ok := f.dbs[db]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, db)
	}
	return tag, nil
}

func (f *Fake) Drop(_ context.Context, _ ConnInfo, db string, tag OwnerTag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Drop", db)
	if f.DropErr != nil {
		return f.DropErr
	}
	current, ok := f.dbs[db]
	if !ok {
		return nil // IF EXISTS
	}
	if current != tag {
		return fmt.Errorf("%w: database %q has comment %q, expected %q", ErrNotOwned, db, current, tag)
	}
	delete(f.dbs, db)
	return nil
}

func (f *Fake) Databases(_ context.Context, _ ConnInfo) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Databases", "")
	if f.ExistsErr != nil {
		return nil, f.ExistsErr
	}
	out := make([]string, 0, len(f.dbs))
	for db := range f.dbs {
		out = append(out, db)
	}
	sort.Strings(out)
	return out, nil
}

func (f *Fake) DropCopy(_ context.Context, _ ConnInfo, db, name string, tag OwnerTag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DropCopy", name)
	if err := CheckCopy(db, name); err != nil {
		return err
	}
	if f.DropErr != nil {
		return f.DropErr
	}
	current, ok := f.dbs[name]
	if !ok {
		return nil
	}
	if err := copyOwned(name, current, tag); err != nil {
		return err
	}
	delete(f.dbs, name)
	return nil
}
