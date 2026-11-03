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
	"math"
	"sync"
	"time"
)

// LimiterConfig holds the per-user and total caps. A zero cap disables it.
type LimiterConfig struct {
	MaxConcurrentPerUser int
	RatePerUser          float64
	RateBurst            int
	MaxTailsPerUser      int
	MaxTails             int
}

// limiter keeps open tails apart from the request concurrency budget: a
// tail holds its slot for minutes, and counting it there would starve the
// same user's dashboard panels.
type limiter struct {
	cfg     LimiterConfig
	now     func() time.Time
	mu      sync.Mutex
	active  map[string]int
	tails   map[string]int
	total   int
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(cfg LimiterConfig) *limiter {
	return &limiter{
		cfg:     cfg,
		now:     time.Now,
		active:  map[string]int{},
		tails:   map[string]int{},
		buckets: map[string]*bucket{},
	}
}

// allowRate takes a token from the user's bucket; when it is empty, it
// returns how long until the next token.
func (l *limiter) allowRate(user string) (time.Duration, bool) {
	if l.cfg.RatePerUser <= 0 {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	burst := float64(max(l.cfg.RateBurst, 1))
	b, ok := l.buckets[user]
	if !ok {
		if len(l.buckets) >= ttlCacheMaxEntries {
			l.sweepBuckets(now, burst)
		}
		b = &bucket{tokens: burst, last: now}
		l.buckets[user] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*l.cfg.RatePerUser)
	b.last = now
	if b.tokens < 1 {
		wait, ok := scaleDuration((1-b.tokens)/l.cfg.RatePerUser, time.Second)
		if !ok {
			wait = math.MaxInt64
		}
		return wait, false
	}
	b.tokens--
	return 0, true
}

// sweepBuckets drops buckets that have refilled completely; recreating one
// later starts it full, which is the same state.
func (l *limiter) sweepBuckets(now time.Time, burst float64) {
	for user, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.cfg.RatePerUser >= burst {
			delete(l.buckets, user)
		}
	}
}

func (l *limiter) acquire(user string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg.MaxConcurrentPerUser > 0 && l.active[user] >= l.cfg.MaxConcurrentPerUser {
		return nil, false
	}
	l.active[user]++
	return l.releaser(func() { decrement(l.active, user) }), true
}

func (l *limiter) acquireTail(user string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg.MaxTailsPerUser > 0 && l.tails[user] >= l.cfg.MaxTailsPerUser {
		return nil, false
	}
	if l.cfg.MaxTails > 0 && l.total >= l.cfg.MaxTails {
		return nil, false
	}
	l.tails[user]++
	l.total++
	return l.releaser(func() {
		decrement(l.tails, user)
		l.total--
	}), true
}

func (l *limiter) openTails() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

func (l *limiter) releaser(release func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			release()
		})
	}
}

func decrement(m map[string]int, user string) {
	if m[user] <= 1 {
		delete(m, user)
		return
	}
	m[user]--
}
