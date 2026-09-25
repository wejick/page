package serve

import (
	"container/list"
	"sync"
	"time"
)

// htmlCache caches entry HTML keyed by slug (D14). Content is immutable, but
// the page's *visibility* is not: parking removes the key, so entries are
// revalidated against storage after a bounded TTL (the serve-side mirror of
// the entry-HTML cache-header policy). Between revalidations, serving is
// memory-only. Misses are never cached (no negative entries); the byte
// budget is a safety valve, not a correctness mechanism.
type htmlCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	ttl      time.Duration
	ll       *list.List               // front = most recent
	items    map[string]*list.Element // slug -> element of *cacheEntry
}

type cacheEntry struct {
	slug        string
	data        []byte
	contentType string
	etag        string
	storedAt    time.Time
}

const (
	defaultCacheMaxBytes = 256 << 20 // 256 MiB
	defaultCacheTTL      = 60 * time.Second
)

func newHTMLCache(maxBytes int64, ttl time.Duration) *htmlCache {
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &htmlCache{
		maxBytes: maxBytes,
		ttl:      ttl,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

// lookup returns the cached entry and whether it is present, plus whether it
// is past its revalidation deadline (stale entries still serve the bytes —
// the caller revalidates via Stat before trusting them).
func (c *htmlCache) lookup(slug string) (e cacheEntry, present, stale bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[slug]
	if !ok {
		return cacheEntry{}, false, false
	}
	c.ll.MoveToFront(el)
	ce := el.Value.(*cacheEntry)
	return cacheEntry{data: ce.data, contentType: ce.contentType, etag: ce.etag},
		true, time.Since(ce.storedAt) > c.ttl
}

// touch marks a revalidated entry fresh again without refetching bytes.
func (c *htmlCache) touch(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[slug]; ok {
		el.Value.(*cacheEntry).storedAt = time.Now()
	}
}

// evict drops an entry whose revalidation proved it gone (parked/removed).
func (c *htmlCache) evict(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[slug]; ok {
		ce := el.Value.(*cacheEntry)
		c.ll.Remove(el)
		delete(c.items, slug)
		c.curBytes -= int64(len(ce.data))
	}
}

func (c *htmlCache) put(slug string, data []byte, contentType, etag string) {
	size := int64(len(data))
	if size > c.maxBytes {
		return // single entry exceeds budget: never cache it
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[slug]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		c.curBytes += size - int64(len(e.data))
		e.data = data
		e.contentType = contentType
		e.etag = etag
		e.storedAt = time.Now()
		return
	}
	e := &cacheEntry{slug: slug, data: data, contentType: contentType, etag: etag, storedAt: time.Now()}
	c.items[slug] = c.ll.PushFront(e)
	c.curBytes += size
	for c.curBytes > c.maxBytes && c.ll.Len() > 1 {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		oe := oldest.Value.(*cacheEntry)
		c.ll.Remove(oldest)
		delete(c.items, oe.slug)
		c.curBytes -= int64(len(oe.data))
	}
}
