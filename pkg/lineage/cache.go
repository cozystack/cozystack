package lineage

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ObjectCache is a small TTL cache shared across WalkOwnershipGraph calls so the
// admission webhook can avoid repeatedly issuing the same uncached dynamic GET to
// the apiserver. A child Pod's owner (StatefulSet, HelmRelease, application CR)
// is looked up on every admission of every sibling — the cache collapses those
// per-process lookups to a single round-trip per TTL window.
//
// The cache stores both successful lookups and negative results (NotFound or
// other transient errors) so a tight stream of admissions for the same parent
// chain does not amplify pressure on the apiserver. Entries expire after `ttl`
// from the time of insertion; staleness is acceptable because the labels we
// compute are derived from immutable identity fields (kind, name, namespace,
// owner refs) — not from object state that mutates frequently.
//
// A nil ObjectCache is a valid no-op cache: Get always misses, Set is a no-op.
type ObjectCache struct {
	ttl   time.Duration
	mu    sync.RWMutex
	items map[cacheKey]cacheEntry
}

type cacheKey struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

type cacheEntry struct {
	obj       *unstructured.Unstructured
	err       error
	expiresAt time.Time
}

// NewObjectCache returns a cache with the given TTL. A ttl of zero or less
// disables caching by returning nil; callers can pass the result straight to
// WalkOwnershipGraph without nil-checking.
func NewObjectCache(ttl time.Duration) *ObjectCache {
	if ttl <= 0 {
		return nil
	}
	return &ObjectCache{
		ttl:   ttl,
		items: make(map[cacheKey]cacheEntry),
	}
}

// Get returns a cached lookup, if any. The second return value reports whether
// the entry was present and unexpired. A nil receiver is safe.
//
// The returned object is the stored pointer, not a copy: the cache is
// process-wide and admissions run concurrently, so callers MUST treat it as
// read-only and DeepCopy before any mutation.
func (c *ObjectCache) Get(apiVersion, kind, namespace, name string) (*unstructured.Unstructured, error, bool) {
	if c == nil {
		return nil, nil, false
	}
	k := cacheKey{apiVersion, kind, namespace, name}
	c.mu.RLock()
	entry, ok := c.items[k]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, nil, false
	}
	return entry.obj, entry.err, true
}

// Set stores a lookup result. Successful objects and permanent NotFound
// errors are cached so that repeated misses do not retry every admission.
// A nil receiver is safe.
func (c *ObjectCache) Set(apiVersion, kind, namespace, name string, obj *unstructured.Unstructured, err error) {
	if c == nil {
		return
	}
	k := cacheKey{apiVersion, kind, namespace, name}
	entry := cacheEntry{
		obj:       obj,
		err:       err,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Lock()
	c.items[k] = entry
	// Bounded sampling eviction: when the map outgrows the threshold, scan a
	// fixed slice of entries (cheap O(1) amortised, ~evictionSampleSize per
	// Set) and drop the expired ones. This avoids the O(N) full-map scan
	// pattern that would otherwise hold the write lock long enough to
	// serialise admission webhook calls under bursty load.
	if len(c.items) > evictionThreshold {
		c.evictExpiredSampleLocked(time.Now(), evictionSampleSize)
	}
	c.mu.Unlock()
}

// evictExpiredSampleLocked scans at most n entries and removes the expired
// ones. The caller must hold c.mu. Map iteration in Go is randomised, so
// repeated invocations probabilistically cover the whole map without ever
// holding the lock for an unbounded amount of time.
func (c *ObjectCache) evictExpiredSampleLocked(now time.Time, n int) {
	if n <= 0 {
		return
	}
	i := 0
	for key, e := range c.items {
		if now.After(e.expiresAt) {
			delete(c.items, key)
		}
		i++
		if i >= n {
			return
		}
	}
}

const (
	// evictionThreshold is the size at which Set begins sampling for expired
	// entries. The sampling bounds the write-lock hold time, not the map
	// size: the map remains bounded only by the number of distinct owners
	// looked up within one TTL window.
	evictionThreshold = 4096
	// evictionSampleSize caps how many entries each Set inspects when
	// evicting. With sampleSize ≪ threshold the work is amortised across many
	// Sets, bounding the write-lock hold time regardless of map size.
	evictionSampleSize = 64
)
