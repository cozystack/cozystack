package lineage

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestObjectCache_NilSafe(t *testing.T) {
	var c *ObjectCache // explicitly nil

	// All operations must be no-ops without panicking.
	if obj, err, ok := c.Get("v1", "Pod", "ns", "n"); ok || obj != nil || err != nil {
		t.Fatalf("nil cache must always miss, got ok=%v obj=%v err=%v", ok, obj, err)
	}
	c.Set("v1", "Pod", "ns", "n", &unstructured.Unstructured{}, nil) // must not panic
}

func TestObjectCache_DisabledByZeroTTL(t *testing.T) {
	if got := NewObjectCache(0); got != nil {
		t.Fatalf("NewObjectCache(0) should return nil to disable caching, got %v", got)
	}
	if got := NewObjectCache(-time.Second); got != nil {
		t.Fatalf("NewObjectCache(<0) should return nil to disable caching, got %v", got)
	}
}

func TestObjectCache_HitMiss(t *testing.T) {
	c := NewObjectCache(time.Minute)

	if _, _, ok := c.Get("v1", "Pod", "ns", "n"); ok {
		t.Fatal("expected miss before Set")
	}

	obj := &unstructured.Unstructured{}
	obj.SetName("foo")
	c.Set("v1", "Pod", "ns", "n", obj, nil)

	got, err, ok := c.Get("v1", "Pod", "ns", "n")
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != obj {
		t.Fatalf("expected cached object pointer to match, got %v", got)
	}
}

func TestObjectCache_NegativeResultIsCached(t *testing.T) {
	c := NewObjectCache(time.Minute)
	want := errors.New("not found")
	c.Set("v1", "Pod", "ns", "n", nil, want)

	got, err, ok := c.Get("v1", "Pod", "ns", "n")
	if !ok {
		t.Fatal("expected hit for negative cached entry")
	}
	if got != nil {
		t.Fatalf("expected nil object for negative entry, got %v", got)
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected cached err to be %v, got %v", want, err)
	}
}

func TestObjectCache_Expires(t *testing.T) {
	c := NewObjectCache(20 * time.Millisecond)
	c.Set("v1", "Pod", "ns", "n", &unstructured.Unstructured{}, nil)

	if _, _, ok := c.Get("v1", "Pod", "ns", "n"); !ok {
		t.Fatal("expected hit immediately after Set")
	}

	time.Sleep(40 * time.Millisecond)

	if _, _, ok := c.Get("v1", "Pod", "ns", "n"); ok {
		t.Fatal("expected miss after TTL elapsed")
	}
}

func TestObjectCache_DistinctKeys(t *testing.T) {
	// Sanity: cacheKey discriminates on every identity field so unrelated lookups
	// don't collide.
	c := NewObjectCache(time.Minute)
	c.Set("v1", "Pod", "ns", "n", mustU("a"), nil)
	c.Set("v1", "Pod", "ns-other", "n", mustU("b"), nil)
	c.Set("v1", "Service", "ns", "n", mustU("c"), nil)
	c.Set("apps/v1", "Pod", "ns", "n", mustU("d"), nil)

	cases := []struct {
		apiVersion, kind, namespace, name string
		wantName                          string
	}{
		{"v1", "Pod", "ns", "n", "a"},
		{"v1", "Pod", "ns-other", "n", "b"},
		{"v1", "Service", "ns", "n", "c"},
		{"apps/v1", "Pod", "ns", "n", "d"},
	}
	for _, tc := range cases {
		got, _, ok := c.Get(tc.apiVersion, tc.kind, tc.namespace, tc.name)
		if !ok {
			t.Fatalf("expected hit for %s/%s in %s/%s", tc.apiVersion, tc.kind, tc.namespace, tc.name)
		}
		if got.GetName() != tc.wantName {
			t.Fatalf("expected obj.name=%s, got %s for %s/%s in %s/%s", tc.wantName, got.GetName(), tc.apiVersion, tc.kind, tc.namespace, tc.name)
		}
	}
}

func TestParseWalkMemory(t *testing.T) {
	// fresh state from no args
	st, err := parseWalkMemory(nil)
	if err != nil || st == nil || st.visited == nil {
		t.Fatalf("expected fresh state, got st=%v err=%v", st, err)
	}

	// legacy map[ObjectID]bool form
	legacy := map[ObjectID]bool{{Name: "x"}: true}
	st, err = parseWalkMemory([]interface{}{legacy})
	if err != nil {
		t.Fatalf("legacy form should accept map[ObjectID]bool, got err=%v", err)
	}
	if !st.visited[ObjectID{Name: "x"}] {
		t.Fatal("legacy visited map should be reused as-is")
	}

	// legacy nil map must not panic on writes
	var nilLegacy map[ObjectID]bool
	st, err = parseWalkMemory([]interface{}{nilLegacy})
	if err != nil {
		t.Fatalf("nil legacy map should be tolerated, got err=%v", err)
	}
	if st.visited == nil {
		t.Fatal("nil legacy map should be replaced with a usable, non-nil map")
	}
	st.visited[ObjectID{Name: "z"}] = true // must not panic

	// preferred *walkState form
	cache := NewObjectCache(time.Minute)
	original := &walkState{visited: map[ObjectID]bool{{Name: "y"}: true}, cache: cache}
	st, err = parseWalkMemory([]interface{}{original})
	if err != nil {
		t.Fatalf("walkState form should be accepted, got err=%v", err)
	}
	if st != original {
		t.Fatal("walkState should be reused by reference so recursion shares cache")
	}

	// invalid type returns fresh state + error
	st, err = parseWalkMemory([]interface{}{"oops"})
	if err == nil {
		t.Fatal("expected error for unsupported memory type")
	}
	if st == nil || st.visited == nil {
		t.Fatal("even on error, returned state must be usable to avoid nil-map panic in callers")
	}
}

func TestObjectCache_EvictionSampleIsBounded(t *testing.T) {
	// Fill the cache far beyond the threshold with expired entries, then call
	// Set once and assert the sweep removed at most evictionSampleSize entries
	// (cheap O(1) amortised) — not the whole map.
	c := NewObjectCache(time.Hour)

	for i := 0; i < evictionThreshold+1000; i++ {
		key := cacheKey{apiVersion: "v1", kind: "Pod", namespace: "ns", name: keyName(i)}
		c.items[key] = cacheEntry{
			obj:       nil,
			err:       nil,
			expiresAt: time.Now().Add(-time.Minute), // expired
		}
	}
	beforeLen := len(c.items)

	// Trigger one Set, which adds 1 and then evicts ≤ evictionSampleSize.
	c.Set("v1", "Pod", "ns", "trigger", mustU("t"), nil)

	delta := beforeLen + 1 - len(c.items)
	if delta < 0 {
		t.Fatalf("expected map to shrink or stay equal after eviction, got delta=%d", delta)
	}
	if delta > evictionSampleSize {
		t.Fatalf("eviction sample is unbounded: removed %d entries in one Set, expected ≤ %d", delta, evictionSampleSize)
	}
}

func keyName(i int) string {
	// avoid pulling in strconv just for tests — base16 is fine.
	const hex = "0123456789abcdef"
	buf := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	for j := 7; j >= 0; j-- {
		buf[j] = hex[i&0xf]
		i >>= 4
	}
	return string(buf)
}

func mustU(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetName(name)
	return u
}
