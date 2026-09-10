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
)

// ErrNotOwned is returned by Drop when the database comment does not match the
// owner tag, i.e. the database was not created by this OdooDeployment.
var ErrNotOwned = errors.New("database is not owned by this OdooDeployment (comment mismatch)")

// ErrNotFound is returned by OwnerTag when the database does not exist.
var ErrNotFound = errors.New("database does not exist")

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
}
