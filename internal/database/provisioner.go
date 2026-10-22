// Package database provisions and drops the PostgreSQL database an
// OdooDeployment runs on. Every database the operator creates is tagged with a
// COMMENT ("odoo-operator:<namespace>/<name>") and Drop refuses to touch a
// database whose comment does not carry that exact tag, so the operator can
// never drop a database it did not create.
package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
)

// ErrNotOwned is returned by Drop when the database comment does not match the
// owner tag, i.e. the database was not created by this OdooDeployment.
var ErrNotOwned = errors.New("database is not owned by this OdooDeployment (comment mismatch)")

// ErrNotFound is returned by OwnerTag when the database does not exist.
var ErrNotFound = errors.New("database does not exist")

// ErrNotCopy is returned by DropCopy for a name that is not a restore's or a
// drill's working copy of the database.
var ErrNotCopy = errors.New("not a restore's or a drill's copy of the database")

// ConnInfo is what the provisioner needs to reach the maintenance database.
type ConnInfo struct {
	Host          string
	Port          int32
	User          string
	Password      string
	MaintenanceDB string
	SSL           bool
}

// OwnerTag is the COMMENT the operator writes on databases it creates:
// "odoo-operator:<namespace>/<name>".
type OwnerTag string

// NewOwnerTag builds the tag for an OdooDeployment.
func NewOwnerTag(namespace, name string) OwnerTag {
	return OwnerTag(fmt.Sprintf("odoo-operator:%s/%s", namespace, name))
}

// Provisioner creates, tags and drops databases.
type Provisioner interface {
	// Exists reports whether the database exists.
	Exists(ctx context.Context, c ConnInfo, db string) (bool, error)
	// Create creates the database, tags it and installs the extensions Odoo
	// likes (best effort).
	Create(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error
	// Tag (re-)applies the owner comment, used after a crash mid-create.
	Tag(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error
	// OwnerTag returns the comment on the database ("" when unset).
	OwnerTag(ctx context.Context, c ConnInfo, db string) (OwnerTag, error)
	// Drop drops the database if and only if its comment equals tag;
	// otherwise it returns ErrNotOwned.
	Drop(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error
	// Databases lists the server's databases (templates left out).
	Databases(ctx context.Context, c ConnInfo) ([]string, error)
	// DropCopy drops name, a working copy a restore or a drill of database
	// db made: the database a restore loads into (<db>_restore_<suffix>)
	// or a drill's scratch database (<db>_drill_<suffix>). Those carry no
	// owner comment, so the name is what is checked: any other name is
	// refused with ErrNotCopy, and so is a copy whose comment is set to
	// anything but tag (ErrNotOwned). A copy that does not exist is no error.
	DropCopy(ctx context.Context, c ConnInfo, db, name string, tag OwnerTag) error
}

// CheckCopy returns nil when name is a restore's or a drill's working copy
// of database db (layout.RestoreName or layout.DrillName for some suffix),
// and an ErrNotCopy otherwise. The live database itself is never one.
func CheckCopy(db, name string) error {
	for _, suffix := range []string{layout.RestoreSuffix(name, db), layout.DrillSuffix(name, db)} {
		if suffix != "" && layout.CheckName(name, db, suffix) == nil {
			return nil
		}
	}
	return fmt.Errorf("%w %q: %q", ErrNotCopy, db, name)
}

// copyOwned reports whether a copy whose comment is current may be dropped
// by the instance tagged tag: copies carry no comment, or that tag.
func copyOwned(db string, current, tag OwnerTag) error {
	if current != "" && current != tag {
		return fmt.Errorf("%w: database %q has comment %q, expected none or %q", ErrNotOwned, db, current, tag)
	}
	return nil
}
