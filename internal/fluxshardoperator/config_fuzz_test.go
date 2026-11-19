package fluxshardoperator

import "testing"

// FuzzParseShardIndex checks that parsing an arbitrary shard-key value never
// panics and that a successful parse round-trips: if ParseShardIndex accepts a
// string it must be the canonical name of the index it returns.
func FuzzParseShardIndex(f *testing.F) {
	for _, seed := range []string{"", "shard0", "shard10", "shard-1", "shard00", "tenants", "shard", "shardx", "shard 1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		i, ok := ParseShardIndex(s)
		if !ok {
			return
		}
		if i < 0 {
			t.Fatalf("ParseShardIndex(%q) returned ok with negative index %d", s, i)
		}
		if got := ShardName(i); got != s {
			t.Fatalf("ParseShardIndex(%q)=%d but ShardName(%d)=%q, not a round-trip", s, i, i, got)
		}
	})
}

// FuzzShardNameRoundTrip checks the inverse direction: every canonical name
// produced by ShardName must parse back to the same non-negative index.
func FuzzShardNameRoundTrip(f *testing.F) {
	for _, seed := range []int{0, 1, 9, 10, 128, 1000} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, i int) {
		if i < 0 {
			return // negative indices are not valid shards; ShardName is only defined for i >= 0
		}
		got, ok := ParseShardIndex(ShardName(i))
		if !ok || got != i {
			t.Fatalf("ShardName(%d) did not round-trip: ParseShardIndex=%d ok=%v", i, got, ok)
		}
	})
}

// FuzzParseShardCount checks that the --shard-count flag parser never panics and
// upholds its contract: a successful non-auto parse yields a positive count, and
// the auto sentinel yields count 0.
func FuzzParseShardCount(f *testing.F) {
	for _, seed := range []string{"", "auto", "AUTO", " auto ", "0", "1", "-1", "3", "  5  ", "abc", "9999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		count, auto, err := ParseShardCount(s)
		if err != nil {
			return
		}
		if auto {
			if count != 0 {
				t.Fatalf("ParseShardCount(%q): auto=true must yield count 0, got %d", s, count)
			}
			return
		}
		if count < 1 {
			t.Fatalf("ParseShardCount(%q): non-auto success must yield count >= 1, got %d", s, count)
		}
	})
}

// FuzzParsePinnedTenants checks that the --pinned-tenants flag parser never
// panics and that every entry in a successful parse is well-formed: a non-empty
// tenant key mapped to a value that is itself a valid shard name.
func FuzzParsePinnedTenants(f *testing.F) {
	for _, seed := range []string{
		"", "a=shard0", "a=shard0,b=shard1", " a = shard0 ", "a=", "=shard0",
		"a=shardx", "a", "a=shard0,", ",", "a=shard0,b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		pinned, err := ParsePinnedTenants(s)
		if err != nil {
			return
		}
		for k, v := range pinned {
			if k == "" {
				t.Fatalf("ParsePinnedTenants(%q) produced an empty tenant key", s)
			}
			if _, ok := ParseShardIndex(v); !ok {
				t.Fatalf("ParsePinnedTenants(%q) mapped %q to invalid shard %q", s, k, v)
			}
		}
	})
}
