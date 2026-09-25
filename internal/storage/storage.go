// Package storage defines the single code seam of this service (design D7):
// five operations over immutable objects. Nothing else in the codebase may
// reference a storage SDK directly.
package storage

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get and Stat when the key does not exist.
var ErrNotFound = errors.New("storage: not found")

// Object is a stored object ready to read.
type Object struct {
	Reader      io.ReadCloser
	ContentType string
	Size        int64
	ETag        string
}

// ObjectMeta describes a stored object without its bytes.
type ObjectMeta struct {
	ContentType string
	Size        int64
	ETag        string
}

// Storage is the entire storage surface of the service. Deliberately frozen:
// no listing, no multipart, no presigned URLs. Copy is prefix-based, mirroring
// DeletePrefix, so callers never enumerate keys (park needs bulk copies).
type Storage interface {
	Put(ctx context.Context, key, contentType string, r io.Reader) error
	Get(ctx context.Context, key string) (Object, error)
	Stat(ctx context.Context, key string) (ObjectMeta, error)
	Copy(ctx context.Context, srcPrefix, dstPrefix string) error
	DeletePrefix(ctx context.Context, prefix string) error
}
