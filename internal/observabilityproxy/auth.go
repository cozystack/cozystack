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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrUnauthenticated means the token is missing or kube-apiserver rejected it.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authorizer asks kube-apiserver who a token belongs to and whether that
// identity may read a scope. Answers, including negative ones, are cached
// for the TTL; transport errors are not, so an apiserver blip is retried on
// the next request instead of locking a user out for the TTL.
type Authorizer struct {
	client    kubernetes.Interface
	now       func() time.Time
	tokens    *ttlCache[*authenticationv1.UserInfo]
	decisions *ttlCache[bool]
}

func NewAuthorizer(client kubernetes.Interface, ttl time.Duration) *Authorizer {
	return &Authorizer{
		client:    client,
		now:       time.Now,
		tokens:    newTTLCache[*authenticationv1.UserInfo](ttl),
		decisions: newTTLCache[bool](ttl),
	}
}

func (a *Authorizer) Authenticate(ctx context.Context, token string) (*authenticationv1.UserInfo, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	if u, ok := a.tokens.get(key, a.now()); ok {
		if u == nil {
			return nil, ErrUnauthenticated
		}
		return u, nil
	}
	tr, err := a.client.AuthenticationV1().TokenReviews().Create(ctx,
		&authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token review: %w", err)
	}
	var u *authenticationv1.UserInfo
	if tr.Status.Authenticated && tr.Status.User.Username != "" {
		u = tr.Status.User.DeepCopy()
	}
	a.tokens.set(key, u, a.now())
	if u == nil {
		return nil, ErrUnauthenticated
	}
	return u, nil
}

func (a *Authorizer) Authorize(ctx context.Context, user *authenticationv1.UserInfo, attrs authorizationv1.ResourceAttributes) (bool, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for k, v := range user.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}
	spec := authorizationv1.SubjectAccessReviewSpec{
		ResourceAttributes: &attrs,
		User:               user.Username,
		UID:                user.UID,
		Groups:             user.Groups,
		Extra:              extra,
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return false, err
	}
	key := string(raw)
	if allowed, ok := a.decisions.get(key, a.now()); ok {
		return allowed, nil
	}
	sar, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx,
		&authorizationv1.SubjectAccessReview{Spec: spec}, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("subject access review: %w", err)
	}
	allowed := sar.Status.Allowed && !sar.Status.Denied
	a.decisions.set(key, allowed, a.now())
	return allowed, nil
}

// ttlCacheMaxEntries bounds memory against a flood of distinct tokens; once
// reached, expired entries are swept and, if that is not enough, the cache
// starts over, which costs only extra reviews.
const ttlCacheMaxEntries = 10000

type ttlEntry[V any] struct {
	value   V
	expires time.Time
}

type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ttlEntry[V]
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, entries: map[string]ttlEntry[V]{}}
}

func (c *ttlCache[V]) get(key string, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[V]) set(key string, v V, now time.Time) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= ttlCacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= ttlCacheMaxEntries {
			c.entries = map[string]ttlEntry[V]{}
		}
	}
	c.entries[key] = ttlEntry[V]{value: v, expires: now.Add(c.ttl)}
}
