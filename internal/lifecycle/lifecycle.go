// Package lifecycle implements the page park/unpark toggle and hard delete:
// hiding a page moves its objects from {slug}/ to the reserved
// _parked/{slug}/ prefix and back, and deleting removes both prefixes and the
// row — so the serve plane keeps doing URL→key arithmetic and learns the
// state purely from key existence. The bucket is the toggle state; the
// database records intent (live/parking/parked/unparking/deleting) so a crash
// mid-move is healed by idempotent retry or the boot sweep. The serve plane
// never queries any of this.
package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"page/internal/db"
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
	StatusDeleting  = "deleting"
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
	db    *sql.DB
	store storage.Storage
	log   *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-slug: serialize toggles in-process
}

// New builds the lifecycle service. log may be nil → slog default.
func New(log *slog.Logger, db *sql.DB, store storage.Storage) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, store: store, log: log, locks: make(map[string]*sync.Mutex)}
}

// placeholders returns n comma-separated ? markers for an IN list.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
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
	args := make([]any, 0, 2+len(prev))
	args = append(args, intent, slug)
	for _, p := range prev {
		args = append(args, p)
	}
	tag, err := s.db.ExecContext(ctx,
		`UPDATE pages SET status = ? WHERE slug = ? AND status IN (`+placeholders(len(prev))+`)`,
		args...)
	if err != nil {
		return "", fmt.Errorf("lifecycle: transition %s: %w", intent, err)
	}
	if n, err := tag.RowsAffected(); err != nil {
		return "", fmt.Errorf("lifecycle: transition %s rows: %w", intent, err)
	} else if n == 0 {
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
	tag, err := s.db.ExecContext(ctx,
		`UPDATE pages SET status = ? WHERE slug = ? AND status = ?`,
		done, slug, intent)
	if err != nil {
		return fmt.Errorf("lifecycle: finalize %s: %w", done, err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifecycle: finalize %s rows: %w", done, err)
	}
	if n != 1 {
		// Only same-direction transitions touch these states, so this is
		// unreachable in practice; fail loudly rather than lie.
		return fmt.Errorf("lifecycle: finalize %s: status moved concurrently", slug)
	}
	return nil
}

// Delete removes a page permanently. The guarded transition to `deleting`
// records intent before any object is removed, so a concurrent park/unpark
// (or a delete racing a toggle) serializes instead of corrupting state; both
// the live and parked prefixes go idempotently (the page occupies one of
// them), and the row goes last — a crash leaves an honest `deleting` row for
// the boot sweep, never a live row with missing objects
// (add-admin-management-ui D2, D3). Re-invoking a Delete whose predecessor
// died mid-flight resumes and converges, like the toggles.
func (s *Service) Delete(ctx context.Context, slug string) error {
	if !validSlug(slug) {
		return ErrNotFound
	}
	unlock := s.lockFor(slug)
	unlock.Lock()
	defer unlock.Unlock()

	tag, err := s.db.ExecContext(ctx,
		`UPDATE pages SET status = ? WHERE slug = ? AND status IN (?, ?, ?)`,
		StatusDeleting, slug, StatusLive, StatusParked, StatusDeleting)
	if err != nil {
		return fmt.Errorf("lifecycle: transition deleting: %w", err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return fmt.Errorf("lifecycle: transition deleting rows: %w", err)
	}
	if n == 0 {
		// Not in a legal start state: find out why.
		cur, err := s.status(ctx, slug)
		if err != nil {
			return err
		}
		if cur == StatusParking || cur == StatusUnparking {
			return ErrBusy
		}
		return ErrNotFound
	}
	return s.finishDelete(ctx, slug)
}

// finishDelete removes both prefixes and the row; every step is idempotent,
// so Delete and the sweep re-run it safely after a crash.
func (s *Service) finishDelete(ctx context.Context, slug string) error {
	if err := s.store.DeletePrefix(ctx, slug+"/"); err != nil {
		return fmt.Errorf("lifecycle: delete %s/: %w", slug, err)
	}
	if err := s.store.DeletePrefix(ctx, ParkedPrefix+slug+"/"); err != nil {
		return fmt.Errorf("lifecycle: delete %s%s/: %w", ParkedPrefix, slug, err)
	}
	if err := db.DeletePage(ctx, s.db, slug); err != nil {
		return fmt.Errorf("lifecycle: remove row: %w", err)
	}
	return nil
}

// Sweep resumes every toggle or delete left mid-flight by a crash: rows
// parked in parking/unparking re-run their (idempotent) move and finalize;
// rows in deleting re-run the (idempotent) prefix and row removal.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, status FROM pages WHERE status IN ('parking', 'unparking', 'deleting')`)
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
		if p.status == StatusDeleting {
			if err := s.finishDelete(ctx, p.slug); err != nil {
				unlock.Unlock()
				return err
			}
			unlock.Unlock()
			s.log.Info("lifecycle", "what", "resumed interrupted delete",
				"slug", p.slug)
			continue
		}
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
		s.log.Info("lifecycle", "what", "resumed interrupted toggle",
			"slug", p.slug, "status", done)
	}
	return nil
}

func (s *Service) status(ctx context.Context, slug string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM pages WHERE slug = ?`, slug).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
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
