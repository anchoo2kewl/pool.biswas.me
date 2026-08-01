// Package store owns the database: connection, migrations, and every query.
// It runs on an embedded Turso database (github.com/tursodatabase/turso-go),
// which needs no server and keeps the whole application in one container.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/tursodatabase/turso-go"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// DB wraps the connection pool.
type DB struct {
	*sql.DB
}

// Open connects to the database at path and applies any pending migrations.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("turso", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// The embedded engine takes a single writer; serialising here avoids
	// write contention surfacing as errors under concurrent requests.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{sqlDB}

	// Note: the embedded engine does not implement `PRAGMA foreign_keys`, so
	// the ON DELETE CASCADE clauses in the schema are documentation rather
	// than behaviour. Deletes that need to cascade do so explicitly — see
	// DeletePool and DeleteTest.

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) migrate() error {
	// schema_migrations is created by the first migration, so a missing table
	// here simply means nothing has been applied yet.
	applied := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM schema_migrations`)
	if err == nil {
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			applied[n] = true
		}
		rows.Close()
	}

	for _, m := range migrations {
		if applied[m.Name] {
			continue
		}
		// The engine executes one statement per call, so split the migration.
		for _, stmt := range splitSQL(m.SQL) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("migration %s: %w\nstatement: %s", m.Name, err, truncate(stmt, 200))
			}
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(name, applied_at) VALUES(?, ?)`, m.Name, Now()); err != nil {
			return fmt.Errorf("record migration %s: %w", m.Name, err)
		}
	}
	return nil
}

// splitSQL breaks a migration into individual statements, ignoring semicolons
// inside string literals and skipping SQL comments.
func splitSQL(s string) []string {
	var out []string
	var cur strings.Builder
	inStr := false
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		if !inStr && strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		for _, r := range line {
			switch {
			case r == '\'':
				inStr = !inStr
				cur.WriteRune(r)
			case r == ';' && !inStr:
				if stmt := strings.TrimSpace(cur.String()); stmt != "" {
					out = append(out, stmt)
				}
				cur.Reset()
			default:
				cur.WriteRune(r)
			}
		}
		cur.WriteRune('\n')
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Now returns the current UTC timestamp in the format used throughout the
// schema (RFC3339, second precision, sortable as text).
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// Today returns the current date as YYYY-MM-DD.
func Today() string { return time.Now().Format("2006-01-02") }

// ctx is a convenience for queries that do not carry a caller context.
func ctx() context.Context { return context.Background() }

// ids runs a query returning a single id column and collects the results. It
// exists because the embedded engine does not support IN (subquery), so
// parent/child deletes have to resolve ids first.
func (db *DB) ids(query string, args ...any) ([]int64, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
