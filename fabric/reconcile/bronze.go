package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/microsoft/go-mssqldb/azuread"
)

// BronzeReader reads current state from the Fabric Lakehouse SQL analytics
// endpoint (read-only by design, which is all reconciliation needs).
type BronzeReader interface {
	CountRows(ctx context.Context, table string) (int64, error)
	ListKeys(ctx context.Context, table, keyColumn string) ([]string, error)
}

type sqlBronzeReader struct {
	db     *sql.DB
	schema string
}

// NewSQLBronzeReader connects to the Fabric Lakehouse SQL analytics endpoint
// using Entra authentication (build brief README prerequisite: an Entra
// identity with Contributor access to the workspace).
func NewSQLBronzeReader(endpoint, schema string) (*sqlBronzeReader, error) {
	db, err := sql.Open(azuread.DriverName, endpoint)
	if err != nil {
		return nil, fmt.Errorf("opening Fabric SQL analytics endpoint: %w", err)
	}
	return &sqlBronzeReader{db: db, schema: schema}, nil
}

func (r *sqlBronzeReader) Close() error { return r.db.Close() }

func (r *sqlBronzeReader) CountRows(ctx context.Context, table string) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM [%s].[%s]", r.schema, table)
	if err := r.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting rows in %s.%s: %w", r.schema, table, err)
	}
	return count, nil
}

func (r *sqlBronzeReader) ListKeys(ctx context.Context, table, keyColumn string) ([]string, error) {
	query := fmt.Sprintf("SELECT [%s] FROM [%s].[%s]", keyColumn, r.schema, table)
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing keys from %s.%s: %w", r.schema, table, err)
	}
	defer func() { _ = rows.Close() }()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scanning key from %s.%s: %w", r.schema, table, err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
