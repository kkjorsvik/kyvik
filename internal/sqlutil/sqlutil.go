package sqlutil

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExecContext executes a query with automatic PostgreSQL placeholder rebinding.
func ExecContext(ctx context.Context, db *sql.DB, query string, args ...any) (sql.Result, error) {
	return db.ExecContext(ctx, Rebind(query), args...)
}

// QueryContext executes a query with automatic PostgreSQL placeholder rebinding.
func QueryContext(ctx context.Context, db *sql.DB, query string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(ctx, Rebind(query), args...)
}

// QueryRowContext executes a query with automatic PostgreSQL placeholder rebinding.
func QueryRowContext(ctx context.Context, db *sql.DB, query string, args ...any) *sql.Row {
	return db.QueryRowContext(ctx, Rebind(query), args...)
}

// BeginTx wraps a sql.Tx with query rebinding behavior.
func BeginTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx}, nil
}

// Tx wraps *sql.Tx to apply Rebind on every query.
type Tx struct {
	raw *sql.Tx
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, Rebind(query), args...)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.raw.QueryContext(ctx, Rebind(query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, Rebind(query), args...)
}

func (t *Tx) Commit() error {
	return t.raw.Commit()
}

func (t *Tx) Rollback() error {
	return t.raw.Rollback()
}

// Rebind converts ? placeholders to $N placeholders if needed.
// Converts ? placeholders to $N for PostgreSQL compatibility.
func Rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	inSingle := false
	inDouble := false
	param := 1
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
			b.WriteByte(ch)
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
			b.WriteByte(ch)
		case '?':
			if inSingle || inDouble {
				b.WriteByte(ch)
				continue
			}
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(param))
			param++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}
