// Package lifecycle implements the page park/unpark toggle: hiding a page
// moves its objects from {slug}/ to the reserved _parked/{slug}/ prefix and
// back, so the serve plane keeps doing URL→key arithmetic and learns the
// state purely from key existence. The bucket is the toggle state; Postgres
// records intent (live/parking/parked/unparking) so a crash mid-move is
// healed by idempotent retry or the boot sweep. The serve plane never
// queries any of this.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"page/internal/storage"
)

// ParkedPrefix is the reserved top-level bucket prefix holding parked pages.
// It cannot collide with slugs: identifier sanitization excludes underscores.
const ParkedPrefix = "_parked/"

// Lifecycle states persisted in pages.status.
const (
	StatusLive      = "live"
	StatusParking   = "parking"
	StatusParked    = "parked"
	StatusUnparking = "unparking"
)

var (
	// ErrNotFound means no page with that slug exists.
	ErrNotFound = errors.New("lifecycle: page not found")
	// ErrBusy means a toggle is running in the opposite direction; retry
	// after it settles rather than racing it.
	ErrBusy = errors.New("lifecycle: opposite toggle in progress")
)

// Service parks and unparks pages.
type Service struct {
	pool  *pgxpool.Pool
	store storage.Storage

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-slug: serialize toggles in-process
}

// New builds the lifecycle service.
func New(pool *pgxpool.Pool, store storage.Storage) *Service {
	return &Service{pool: pool, store: store, locks: make(map[string]*sync.Mutex)}
}

// lockFor serializes toggles of one slug so a same-direction re-invoke
// queues behind the in-flight move (then lands idempotently) instead of
// interleaving two sweeps over the same objects.
func (s *Service) lockFor(slug string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.locks[slug]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.locks[slug] = m
	return m
}

// Park hides a page: copies {slug}/ under _parked/{slug}/, deletes the
// original prefix, and marks the page parked. Idempotent when already parked.
func (s *Service) Park(ctx context.Context, slug string) (string, error) {
	return s.toggle(ctx, slug, StatusParking, StatusParked,
		[]string{StatusLive, StatusParking})
}

// Unpark restores a parked page at its original URL with identical bytes.
// Idempotent when already live.
func (s *Service) Unpark(ctx context.Context, slug string) (string, error) {
	return s.toggle(ctx, slug, StatusUnparking, StatusLive,
		[]string{StatusParked, StatusUnparking})
}

// toggle runs one direction of the state machine. intent is the transient
// state recorded before any object moves; done is the terminal state; prev
// lists the statuses this direction may start from (its own transient
// included, so an interrupted run resumes instead of erroring).
func (s *Service) toggle(ctx context.Context, slug, intent, done string, prev []string) (string, error) {
	if !validSlug(slug) {
		return "", ErrNotFound
	}
	unlock := s.lockFor(slug)
	unlock.Lock()
	defer unlock.Unlock()

	// Guarded transition: only from a legal previous state, so concurrent
	// opposite toggles serialize instead of racing.
	tag, err := s.pool.Exec(ctx,
		`UPDATE pages SET status = $1 WHERE slug = $2 AND status = ANY($3)`,
		intent, slug, prev)
	if err != nil {
		return "", fmt.Errorf("lifecycle: transition %s: %w", intent, err)
	}
	if tag.RowsAffected() == 0 {
		// Not in a legal start state: find out why.
		cur, err := s.status(ctx, slug)
		if err != nil {
			return "", err
		}
		if cur == done {
			return done, nil // already there: idempotent
		}
		if cur == opposite(intent) {
			return "", ErrBusy
		}
		return "", ErrNotFound
	}

	if err := s.move(ctx, slug, intent); err != nil {
		// Status stays at the intent state: the boot sweep or a retry of
		// this same call converges from here (copies and deletes are
		// idempotent; copy-before-delete never destroys the only copy).
		return "", err
	}
	if err := s.finalize(ctx, slug, intent, done); err != nil {
		return "", err
	}
	return done, nil
}

// move performs the copy-before-delete sweep for one direction.
func (s *Service) move(ctx context.Context, slug, intent string) error {
	live := slug + "/"
	parked := ParkedPrefix + slug + "/"
	src, dst := live, parked
	if intent == StatusUnparking {
		src, dst = parked, live
	}
	if err := s.store.Copy(ctx, src, dst); err != nil {
		return fmt.Errorf("lifecycle: copy %s→%s: %w", src, dst, err)
	}
	if err := s.store.DeletePrefix(ctx, src); err != nil {
		return fmt.Errorf("lifecycle: delete %s: %w", src, err)
	}
	return nil
}

func (s *Service) finalize(ctx context.Context, slug, intent, done string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE pages SET status = $1 WHERE slug = $2 AND status = $3`,
		done, slug, intent)
	if err != nil {
		return fmt.Errorf("lifecycle: finalize %s: %w", done, err)
	}
	if tag.RowsAffected() != 1 {
		// Only same-direction transitions touch these states, so this is
		// unreachable in practice; fail loudly rather than lie.
		return fmt.Errorf("lifecycle: finalize %s: status moved concurrently", slug)
	}
	return nil
}

// Sweep resumes every toggle left mid-flight by a crash: rows parked in
// parking/unparking re-run their (idempotent) move and finalize.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.pool.Query(ctx,
		`SELECT slug, status FROM pages WHERE status = ANY('{parking,unparking}')`)
	if err != nil {
		return fmt.Errorf("lifecycle: sweep query: %w", err)
	}
	type pending struct {
		slug   string
		status string
	}
	var todos []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.slug, &p.status); err != nil {
			rows.Close()
			return fmt.Errorf("lifecycle: sweep scan: %w", err)
		}
		todos = append(todos, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("lifecycle: sweep rows: %w", err)
	}

	for _, p := range todos {
		unlock := s.lockFor(p.slug)
		unlock.Lock()
		if err := s.move(ctx, p.slug, p.status); err != nil {
			unlock.Unlock()
			return err
		}
		done := StatusParked
		if p.status == StatusUnparking {
			done = StatusLive
		}
		if err := s.finalize(ctx, p.slug, p.status, done); err != nil {
			unlock.Unlock()
			return err
		}
		unlock.Unlock()
		slog.Info("lifecycle: resumed interrupted toggle",
			"slug", p.slug, "status", done)
	}
	return nil
}

func (s *Service) status(ctx context.Context, slug string) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM pages WHERE slug = $1`, slug).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lifecycle: read status: %w", err)
	}
	return status, nil
}

func opposite(intent string) string {
	if intent == StatusParking {
		return StatusUnparking
	}
	return StatusParking
}

// validSlug matches the serve plane's slug segment rule: one clean path
// segment, so the slug can never escape its prefix or forge _parked/.
func validSlug(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}
