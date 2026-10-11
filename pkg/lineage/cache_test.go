package lineage

import (
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestObjectCache_NilSafe(t *testing.T) {
	var c *ObjectCache

	if obj, ok := c.Get("v1", "Pod", "ns", "n"); ok || obj != nil {
		t.Fatalf("nil cache must always miss, got ok=%v obj=%v", ok, obj)
	}
	c.Set("v1", "Pod", "ns", "n", &unstructured.Unstructured{}) // must not panic
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

	if _, ok := c.Get("v1", "Pod", "ns", "n"); ok {
		t.Fatal("expected miss before Set")
	}

	obj := &unstructured.Unstructured{}
	obj.SetName("foo")
	c.Set("v1", "Pod", "ns", "n", obj)

	got, ok := c.Get("v1", "Pod", "ns", "n")
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if got != obj {
		t.Fatalf("expected cached object pointer to match, got %v", got)
	}
}

func TestObjectCache_Expires(t *testing.T) {
	c := NewObjectCache(20 * time.Millisecond)
	c.Set("v1", "Pod", "ns", "n", &unstructured.Unstructured{})

	if _, ok := c.Get("v1", "Pod", "ns", "n"); !ok {
		t.Fatal("expected hit immediately after Set")
	}

	time.Sleep(40 * time.Millisecond)

	if _, ok := c.Get("v1", "Pod", "ns", "n"); ok {
		t.Fatal("expected miss after TTL elapsed")
	}
}

func TestObjectCache_DistinctKeys(t *testing.T) {
	c := NewObjectCache(time.Minute)
	c.Set("v1", "Pod", "ns", "n", mustU("a"))
	c.Set("v1", "Pod", "ns-other", "n", mustU("b"))
	c.Set("v1", "Service", "ns", "n", mustU("c"))
	c.Set("apps/v1", "Pod", "ns", "n", mustU("d"))

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
		got, ok := c.Get(tc.apiVersion, tc.kind, tc.namespace, tc.name)
		if !ok {
			t.Fatalf("expected hit for %s/%s in %s/%s", tc.apiVersion, tc.kind, tc.namespace, tc.name)
		}
		if got.GetName() != tc.wantName {
			t.Fatalf("expected obj.name=%s, got %s for %s/%s in %s/%s", tc.wantName, got.GetName(), tc.apiVersion, tc.kind, tc.namespace, tc.name)
		}
	}
}

func TestParseWalkMemory(t *testing.T) {
	st, err := parseWalkMemory(nil)
	if err != nil || st == nil || st.visited == nil {
		t.Fatalf("expected fresh state, got st=%v err=%v", st, err)
	}

	legacy := map[ObjectID]bool{{Name: "x"}: true}
	st, err = parseWalkMemory([]any{legacy})
	if err != nil {
		t.Fatalf("legacy form should accept map[ObjectID]bool, got err=%v", err)
	}
	if !st.visited[ObjectID{Name: "x"}] {
		t.Fatal("legacy visited map should be reused as-is")
	}

	var nilLegacy map[ObjectID]bool
	st, err = parseWalkMemory([]any{nilLegacy})
	if err != nil {
		t.Fatalf("nil legacy map should be tolerated, got err=%v", err)
	}
	if st.visited == nil {
		t.Fatal("nil legacy map should be replaced with a usable, non-nil map")
	}
	st.visited[ObjectID{Name: "z"}] = true // must not panic

	cache := NewObjectCache(time.Minute)
	original := &walkState{visited: map[ObjectID]bool{{Name: "y"}: true}, cache: cache}
	st, err = parseWalkMemory([]any{original})
	if err != nil {
		t.Fatalf("walkState form should be accepted, got err=%v", err)
	}
	if st != original {
		t.Fatal("walkState should be reused by reference so recursion shares cache")
	}

	st, err = parseWalkMemory([]any{"oops"})
	if err == nil {
		t.Fatal("expected error for unsupported memory type")
	}
	if st == nil || st.visited == nil {
		t.Fatal("even on error, returned state must be usable to avoid nil-map panic in callers")
	}
}

func TestObjectCache_EvictionSampleIsBounded(t *testing.T) {
	c := NewObjectCache(time.Hour)

	for i := range evictionThreshold + 1000 {
		key := cacheKey{apiVersion: "v1", kind: "Pod", namespace: "ns", name: strconv.Itoa(i)}
		c.items[key] = cacheEntry{
			obj:       nil,
			expiresAt: time.Now().Add(-time.Minute),
		}
	}
	beforeLen := len(c.items)

	c.Set("v1", "Pod", "ns", "trigger", mustU("t"))

	delta := beforeLen + 1 - len(c.items)
	if delta < 0 {
		t.Fatalf("expected map to shrink or stay equal after eviction, got delta=%d", delta)
	}
	if delta > evictionSampleSize {
		t.Fatalf("eviction sample is unbounded: removed %d entries in one Set, expected at most %d", delta, evictionSampleSize)
	}
}

func mustU(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetName(name)
	return u
}
