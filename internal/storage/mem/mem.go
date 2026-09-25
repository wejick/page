// Package mem is the in-memory storage driver used by unit tests: a real
// implementation of the Storage interface, not a mock of it.
package mem

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"

	"page/internal/storage"
)

type object struct {
	data        []byte
	contentType string
	etag        string
}

// Store keeps objects in a map behind an RWMutex.
type Store struct {
	mu   sync.RWMutex
	objs map[string]object
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{objs: make(map[string]object)}
}

func (s *Store) Put(_ context.Context, key, contentType string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("mem: read: %w", err)
	}
	sum := md5.Sum(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = object{
		data:        bytes.Clone(data),
		contentType: contentType,
		etag:        hex.EncodeToString(sum[:]),
	}
	return nil
}

func (s *Store) Get(_ context.Context, key string) (storage.Object, error) {
	s.mu.RLock()
	obj, ok := s.objs[key]
	s.mu.RUnlock()
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{
		Reader:      io.NopCloser(bytes.NewReader(obj.data)),
		ContentType: obj.contentType,
		Size:        int64(len(obj.data)),
		ETag:        obj.etag,
	}, nil
}

func (s *Store) Stat(_ context.Context, key string) (storage.ObjectMeta, error) {
	s.mu.RLock()
	obj, ok := s.objs[key]
	s.mu.RUnlock()
	if !ok {
		return storage.ObjectMeta{}, storage.ErrNotFound
	}
	return storage.ObjectMeta{ContentType: obj.contentType, Size: int64(len(obj.data)), ETag: obj.etag}, nil
}

// Len returns the number of stored objects (test introspection).
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.objs)
}

// Copy copies every object under srcPrefix to the corresponding key under
// dstPrefix (page parking). Sources are untouched; existing destination keys
// are overwritten. An empty source prefix is a no-op guard.
func (s *Store) Copy(_ context.Context, srcPrefix, dstPrefix string) error {
	if srcPrefix == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Collect first: writing during range could visit freshly written keys
	// when the prefixes nest.
	type pair struct {
		key string
		obj object
	}
	pairs := make([]pair, 0, 8)
	for k, obj := range s.objs {
		if strings.HasPrefix(k, srcPrefix) {
			pairs = append(pairs, pair{dstPrefix + k[len(srcPrefix):], obj})
		}
	}
	for _, p := range pairs {
		p.obj.data = bytes.Clone(p.obj.data)
		s.objs[p.key] = p.obj
	}
	return nil
}

func (s *Store) DeletePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			delete(s.objs, k)
		}
	}
	return nil
}
