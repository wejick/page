// Package slug implements the {identifier}-{code} scheme (design D6):
// sanitized user identifier, system-assigned per-identifier counter,
// composed slug stored as separate columns. Always append, never parse.
package slug

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxIdentifierLen caps the sanitized identifier.
const MaxIdentifierLen = 64

var (
	ErrInvalidIdentifier = errors.New("slug: identifier sanitizes to empty")
	ErrReserved          = errors.New("slug: identifier is reserved")
)

// Reserved identifiers would collide with routing prefixes (design D6).
var Reserved = map[string]bool{
	"api": true, "a": true, "p": true, "ui": true, "www": true,
	"assets": true, "cdn": true, "static": true, "healthz": true,
}

// badChars matches anything that is not a lowercase letter, digit, or dash.
var badChars = regexp.MustCompile(`[^a-z0-9-]+`)

// Sanitize normalizes free-form user input to an identifier:
// lowercase, [a-z0-9-], collapsed/trimmed dashes, length-capped.
func Sanitize(s string) (string, error) {
	out := badChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if len(out) > MaxIdentifierLen {
		out = strings.TrimRight(out[:MaxIdentifierLen], "-")
	}
	if out == "" {
		return "", ErrInvalidIdentifier
	}
	return out, nil
}

// Validate returns an error for reserved identifiers.
func Validate(identifier string) error {
	if Reserved[identifier] {
		return fmt.Errorf("%w: %q", ErrReserved, identifier)
	}
	return nil
}

// Compose builds the full slug from its parts.
func Compose(identifier string, code int) string {
	return fmt.Sprintf("%s-%d", identifier, code)
}

// Allocate returns the next code for identifier, atomically, starting at 1.
// The upsert is the concurrency guarantee; the unique index on
// pages(identifier, code) is the backstop.
func Allocate(ctx context.Context, pool *pgxpool.Pool, identifier string) (int, error) {
	var code int
	err := pool.QueryRow(ctx, `
		INSERT INTO counters (identifier, next) VALUES ($1, 2)
		ON CONFLICT (identifier) DO UPDATE SET next = counters.next + 1
		RETURNING next - 1`, identifier,
	).Scan(&code)
	if err != nil {
		return 0, fmt.Errorf("slug: allocate for %q: %w", identifier, err)
	}
	return code, nil
}

// New sanitizes + validates the identifier and allocates its next code.
// It does not insert the page row; the caller owns that transaction boundary.
func New(ctx context.Context, pool *pgxpool.Pool, rawIdentifier string) (string, int, error) {
	identifier, err := Sanitize(rawIdentifier)
	if err != nil {
		return "", 0, err
	}
	if err := Validate(identifier); err != nil {
		return "", 0, err
	}
	code, err := Allocate(ctx, pool, identifier)
	if err != nil {
		return "", 0, err
	}
	return Compose(identifier, code), code, nil
}

// IsUniqueViolation reports whether err is a Postgres unique violation (23505).
func IsUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
