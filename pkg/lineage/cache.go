package lineage

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ObjectCache holds owner objects fetched during ownership walks, so sibling
// admissions under the same owner chain share one GET per TTL window.
//
// Only successful lookups are stored. A failed lookup leaves the walk without
// an owner, so the webhook admits the object with no lineage labels, and the
// object stays unlabeled until its next UPDATE runs the walk again. Secrets,
// Services and PVCs can go a long time between updates, so a cached failure
// would outlive the TTL on every object admitted meanwhile.
//
// A nil ObjectCache is valid: Get always misses and Set does nothing.
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
	expiresAt time.Time
}

// NewObjectCache returns nil, a disabled cache, when ttl is not positive.
func NewObjectCache(ttl time.Duration) *ObjectCache {
	if ttl <= 0 {
		return nil
	}
	return &ObjectCache{
		ttl:   ttl,
		items: make(map[cacheKey]cacheEntry),
	}
}

// Get returns the stored pointer, shared with concurrent admissions: callers
// must DeepCopy before mutating it.
func (c *ObjectCache) Get(apiVersion, kind, namespace, name string) (*unstructured.Unstructured, bool) {
	if c == nil {
		return nil, false
	}
	k := cacheKey{apiVersion, kind, namespace, name}
	c.mu.RLock()
	entry, ok := c.items[k]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.obj, true
}

func (c *ObjectCache) Set(apiVersion, kind, namespace, name string, obj *unstructured.Unstructured) {
	if c == nil {
		return
	}
	k := cacheKey{apiVersion, kind, namespace, name}
	entry := cacheEntry{
		obj:       obj,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Lock()
	c.items[k] = entry
	if len(c.items) > evictionThreshold {
		c.evictExpiredSampleLocked(time.Now(), evictionSampleSize)
	}
	c.mu.Unlock()
}

// evictExpiredSampleLocked removes the expired entries among the first n that
// map iteration yields. Go randomises that order, so repeated calls cover the
// whole map without a full scan under the write lock. The caller holds c.mu.
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
	// Nothing is evicted below evictionThreshold entries, and Get does not
	// drop expired ones, so up to this many owner objects (HelmReleases and
	// application CRs, values included) stay in memory for the life of the
	// process, expired or not.
	evictionThreshold = 4096
	// evictionSampleSize caps the entries one Set inspects above the
	// threshold, which bounds the write-lock hold time.
	evictionSampleSize = 64
)
