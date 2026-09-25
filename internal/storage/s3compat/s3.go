// Package s3compat speaks the S3 wire protocol to any S3-compatible endpoint
// (MinIO, AWS, R2, B2, …) via minio-go. Driver selection is env-driven (D7).
package s3compat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"page/internal/config"
	"page/internal/storage"
)

// Store is an S3-compatible storage driver.
type Store struct {
	client *minio.Client
	bucket string
}

// New builds a driver from storage config. Path-style is always enabled for
// explicit endpoints (MinIO); S3_SECURE selects https.
func New(cfg config.Storage) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
	})
	if err != nil {
		return nil, fmt.Errorf("s3compat: client: %w", err)
	}
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// EnsureBucket creates the bucket if missing (dev convenience; prod uses IaC).
func (s *Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3compat: bucket exists: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("s3compat: make bucket: %w", err)
	}
	return nil
}

func (s *Store) Put(ctx context.Context, key, contentType string, r io.Reader) error {
	// Pass an explicit size when the reader exposes one: minio-go uploads
	// unknown-size streams as multipart, whose ETags are not content MD5 and
	// would not survive CopyObject (page parking requires stable ETags).
	// Our writers always use in-memory readers, so this is the normal path.
	var size int64 = -1
	switch v := r.(type) {
	case *bytes.Reader:
		size = int64(v.Len())
	case *bytes.Buffer:
		size = int64(v.Len())
	case *strings.Reader:
		size = int64(v.Len())
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("s3compat: put %s: %w", key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key string) (storage.Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return storage.Object{}, notFound(key, err)
	}
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return storage.Object{}, notFound(key, err)
	}
	return storage.Object{
		Reader:      obj,
		ContentType: st.ContentType,
		Size:        st.Size,
		ETag:        strings.Trim(st.ETag, `"`),
	}, nil
}

func (s *Store) Stat(ctx context.Context, key string) (storage.ObjectMeta, error) {
	st, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return storage.ObjectMeta{}, notFound(key, err)
	}
	return storage.ObjectMeta{
		ContentType: st.ContentType,
		Size:        st.Size,
		ETag:        strings.Trim(st.ETag, `"`),
	}, nil
}

// Copy copies every object under srcPrefix to dstPrefix (page parking).
// Listing happens inside the driver — the interface stays five operations.
// Copies are server-side (CopyObject) and bounded in concurrency; existing
// destination keys are overwritten. An empty source prefix is a no-op guard.
func (s *Store) Copy(ctx context.Context, srcPrefix, dstPrefix string) error {
	if srcPrefix == "" {
		return nil
	}
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    srcPrefix,
		Recursive: true,
	})

	const maxInFlight = 8
	sem := make(chan struct{}, maxInFlight)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	fail := func(key string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = fmt.Errorf("s3compat: copy %s: %w", key, err)
		}
	}

	for obj := range objects { // always drain: the list channel is fed by a goroutine
		if obj.Err != nil {
			fail(srcPrefix, obj.Err)
			continue
		}
		dst := dstPrefix + strings.TrimPrefix(obj.Key, srcPrefix)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			fail(obj.Key, ctx.Err())
			continue
		}
		wg.Add(1)
		go func(srcKey, dstKey string) {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := s.client.CopyObject(ctx,
				minio.CopyDestOptions{Bucket: s.bucket, Object: dstKey},
				minio.CopySrcOptions{Bucket: s.bucket, Object: srcKey},
			)
			if err != nil {
				fail(srcKey, err)
			}
		}(obj.Key, dst)
	}
	wg.Wait()
	return firstErr
}

func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	// List under the hood, but only inside the driver: the interface stays
	// four operations (D7).
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	for obj := range objects {
		if obj.Err != nil {
			return fmt.Errorf("s3compat: list %s: %w", prefix, obj.Err)
		}
		if err := s.client.RemoveObject(ctx, s.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			return fmt.Errorf("s3compat: remove %s: %w", obj.Key, err)
		}
	}
	return nil
}

// notFound maps driver errors to storage.ErrNotFound. MinIO/S3 return 403 for
// missing keys on some code paths, so both are translated (design D14).
func notFound(key string, err error) error {
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" ||
		resp.StatusCode == 404 || resp.StatusCode == 403 {
		return fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	return fmt.Errorf("s3compat: %s: %w", key, err)
}

var _ storage.Storage = (*Store)(nil)
