package database

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Pgx is the pgx-backed Provisioner.
type Pgx struct{}

// NewPgx returns a Provisioner that talks to PostgreSQL with pgx.
func NewPgx() *Pgx {
	return &Pgx{}
}

var _ Provisioner = (*Pgx)(nil)

// DSN renders the connection URL for a database on c. User and password are
// URL-escaped; the simple protocol keeps DDL and PgBouncer happy.
func DSN(c ConnInfo, database string) string {
	sslmode := "disable"
	if c.SSL {
		sslmode = "require"
	}
	q := url.Values{}
	q.Set("sslmode", sslmode)
	q.Set("connect_timeout", "10")
	q.Set("application_name", "odoo-operator")
	q.Set("default_query_exec_mode", "simple_protocol")
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))),
		Path:     "/" + database,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// quoteIdentifier double-quotes a database name for DDL.
func quoteIdentifier(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

func (p *Pgx) connect(ctx context.Context, c ConnInfo, database string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, DSN(c, database))
	if err != nil {
		return nil, fmt.Errorf("connect to %s/%s as %s: %w", c.Host, database, c.User, err)
	}
	return conn, nil
}

func (p *Pgx) Exists(ctx context.Context, c ConnInfo, db string) (bool, error) {
	conn, err := p.connect(ctx, c, c.MaintenanceDB)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(ctx) }()

	var one int
	err = conn.QueryRow(ctx, "SELECT 1 FROM pg_database WHERE datname = $1", db).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query pg_database for %q: %w", db, err)
	}
	return true, nil
}

func (p *Pgx) Create(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error {
	conn, err := p.connect(ctx, c, c.MaintenanceDB)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	// Mirrors Odoo's _create_empty_database.
	ddl := fmt.Sprintf(
		"CREATE DATABASE %s ENCODING 'unicode' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0",
		quoteIdentifier(db),
	)
	if _, err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create database %q: %w", db, err)
	}
	if err := tagDatabase(ctx, conn, db, tag); err != nil {
		return err
	}
	_ = conn.Close(ctx)

	// Extensions are best effort: they need CREATE on the database, which the
	// owner has, but a hardened server may still refuse.
	logger := log.FromContext(ctx)
	dbConn, err := p.connect(ctx, c, db)
	if err != nil {
		logger.Info("could not connect to the new database to install extensions", "database", db, "error", err.Error())
		return nil
	}
	defer func() { _ = dbConn.Close(ctx) }()
	for _, ext := range []string{"unaccent", "pg_trgm"} {
		if _, err := dbConn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+quoteIdentifier(ext)); err != nil {
			logger.Info("could not install extension", "database", db, "extension", ext, "error", err.Error())
		}
	}
	return nil
}

func tagDatabase(ctx context.Context, conn *pgx.Conn, db string, tag OwnerTag) error {
	// COMMENT ON does not accept parameters; the tag is operator-generated and
	// contains only a namespace and a name, but quote it anyway.
	stmt := fmt.Sprintf("COMMENT ON DATABASE %s IS %s", quoteIdentifier(db), quoteLiteral(string(tag)))
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("comment on database %q: %w", db, err)
	}
	return nil
}

// quoteLiteral single-quotes a string literal for SQL.
func quoteLiteral(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

func (p *Pgx) Tag(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error {
	conn, err := p.connect(ctx, c, c.MaintenanceDB)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return tagDatabase(ctx, conn, db, tag)
}

func (p *Pgx) OwnerTag(ctx context.Context, c ConnInfo, db string) (OwnerTag, error) {
	conn, err := p.connect(ctx, c, c.MaintenanceDB)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	return ownerTag(ctx, conn, db)
}

func ownerTag(ctx context.Context, conn *pgx.Conn, db string) (OwnerTag, error) {
	var comment *string
	err := conn.QueryRow(ctx,
		"SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1", db,
	).Scan(&comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: %q", ErrNotFound, db)
	}
	if err != nil {
		return "", fmt.Errorf("read comment of database %q: %w", db, err)
	}
	if comment == nil {
		return "", nil
	}
	return OwnerTag(*comment), nil
}

func (p *Pgx) Drop(ctx context.Context, c ConnInfo, db string, tag OwnerTag) error {
	conn, err := p.connect(ctx, c, c.MaintenanceDB)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	current, err := ownerTag(ctx, conn, db)
	if errors.Is(err, ErrNotFound) {
		return nil // already gone: IF EXISTS semantics
	}
	if err != nil {
		return err
	}
	if current != tag {
		return fmt.Errorf("%w: database %q has comment %q, expected %q", ErrNotOwned, db, current, tag)
	}
	// WITH (FORCE) needs PostgreSQL 13+ and terminates the sessions of the
	// same role (the Odoo pods, which are already stopped at this point).
	stmt := fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", quoteIdentifier(db))
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("drop database %q: %w", db, err)
	}
	return nil
}
