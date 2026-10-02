package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrumented wraps a Storage implementation, recording one op counter and
// one duration histogram per operation (observability D6). It is a concrete
// decorator over the existing seam, not a new seam: labels are the frozen op
// names and the outcome class (ok | not_found | error), never keys or
// prefixes — cardinality stays bounded.
type Instrumented struct {
	next Storage

	ops metric.Int64Counter     // storage.ops: op, outcome
	dur metric.Float64Histogram // storage.duration: op
}

// Instrument wraps next with metrics recorded through m.
func Instrument(next Storage, m metric.Meter) (*Instrumented, error) {
	ops, err := m.Int64Counter("storage.ops",
		metric.WithDescription("Storage seam operations by operation and outcome"))
	if err != nil {
		return nil, err
	}
	dur, err := m.Float64Histogram("storage.duration",
		metric.WithDescription("Storage operation duration in seconds"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	return &Instrumented{next: next, ops: ops, dur: dur}, nil
}

// outcome classifies an operation result into the fixed label set.
func outcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	default:
		return "error"
	}
}

// record adds one op observation. durs records only the operation name —
// the outcome is on the counter, keeping histogram label sets small.
func (s *Instrumented) record(ctx context.Context, op string, err error, start time.Time) {
	s.ops.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op), attribute.String("outcome", outcome(err))))
	s.dur.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("op", op)))
}

func (s *Instrumented) Put(ctx context.Context, key, contentType string, r io.Reader) error {
	start := time.Now()
	err := s.next.Put(ctx, key, contentType, r)
	s.record(ctx, "put", err, start)
	return err
}

func (s *Instrumented) Get(ctx context.Context, key string) (Object, error) {
	start := time.Now()
	obj, err := s.next.Get(ctx, key)
	s.record(ctx, "get", err, start)
	return obj, err
}

func (s *Instrumented) Stat(ctx context.Context, key string) (ObjectMeta, error) {
	start := time.Now()
	meta, err := s.next.Stat(ctx, key)
	s.record(ctx, "stat", err, start)
	return meta, err
}

func (s *Instrumented) Copy(ctx context.Context, srcPrefix, dstPrefix string) error {
	start := time.Now()
	err := s.next.Copy(ctx, srcPrefix, dstPrefix)
	s.record(ctx, "copy", err, start)
	return err
}

func (s *Instrumented) DeletePrefix(ctx context.Context, prefix string) error {
	start := time.Now()
	err := s.next.DeletePrefix(ctx, prefix)
	s.record(ctx, "delete_prefix", err, start)
	return err
}
