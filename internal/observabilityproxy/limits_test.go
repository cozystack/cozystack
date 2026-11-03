/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package observabilityproxy

import (
	"testing"
	"time"
)

func TestLimiterConcurrency(t *testing.T) {
	l := newLimiter(LimiterConfig{MaxConcurrentPerUser: 2})
	r1, ok1 := l.acquire("alice")
	_, ok2 := l.acquire("alice")
	if !ok1 || !ok2 {
		t.Fatal("first two requests refused")
	}
	if _, ok := l.acquire("alice"); ok {
		t.Fatal("third concurrent request admitted")
	}
	if _, ok := l.acquire("bob"); !ok {
		t.Fatal("another user is limited by alice's requests")
	}
	r1()
	r1()
	if _, ok := l.acquire("alice"); !ok {
		t.Fatal("slot not returned on release")
	}
	if _, ok := l.acquire("alice"); ok {
		t.Fatal("double release freed a second slot")
	}
}

func TestLimiterTails(t *testing.T) {
	l := newLimiter(LimiterConfig{MaxConcurrentPerUser: 1, MaxTailsPerUser: 2, MaxTails: 3})
	if _, ok := l.acquire("alice"); !ok {
		t.Fatal("request refused")
	}
	a1, ok1 := l.acquireTail("alice")
	_, ok2 := l.acquireTail("alice")
	if !ok1 || !ok2 {
		t.Fatal("tails share the request concurrency budget")
	}
	if _, ok := l.acquireTail("alice"); ok {
		t.Fatal("per-user tail cap not enforced")
	}
	if _, ok := l.acquireTail("bob"); !ok {
		t.Fatal("bob refused below the total cap")
	}
	if _, ok := l.acquireTail("carol"); ok {
		t.Fatal("total tail cap not enforced")
	}
	if got := l.openTails(); got != 3 {
		t.Fatalf("openTails = %d, want 3", got)
	}
	a1()
	if _, ok := l.acquireTail("carol"); !ok {
		t.Fatal("total slot not returned on release")
	}
}

func TestLimiterRate(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(LimiterConfig{RatePerUser: 2, RateBurst: 2})
	l.now = func() time.Time { return now }
	if !allowed(l, "alice") || !allowed(l, "alice") {
		t.Fatal("burst refused")
	}
	if wait, ok := l.allowRate("alice"); ok || wait != 500*time.Millisecond {
		t.Fatalf("request over the burst: admitted %v, wait %s, want refused with 500ms", ok, wait)
	}
	if !allowed(l, "bob") {
		t.Fatal("bob limited by alice's rate")
	}
	now = now.Add(500 * time.Millisecond)
	if !allowed(l, "alice") {
		t.Fatal("token not refilled after 1/rate")
	}
	now = now.Add(100 * time.Millisecond)
	if wait, ok := l.allowRate("alice"); ok || wait != 400*time.Millisecond {
		t.Fatalf("partly refilled bucket: admitted %v, wait %s, want refused with 400ms", ok, wait)
	}
}

func TestLimiterRateDisabled(t *testing.T) {
	l := newLimiter(LimiterConfig{})
	for range 100 {
		if !allowed(l, "alice") {
			t.Fatal("rate limit applied while disabled")
		}
	}
}

func allowed(l *limiter, user string) bool {
	_, ok := l.allowRate(user)
	return ok
}

func TestLimiterRateWaitAtTinyRate(t *testing.T) {
	l := newLimiter(LimiterConfig{RatePerUser: 1e-12, RateBurst: 1})
	allowed(l, "alice")
	if wait, ok := l.allowRate("alice"); ok || wait <= 0 {
		t.Fatalf("empty bucket at a tiny rate: admitted %v, wait %s, want refused with a positive wait", ok, wait)
	}
}
