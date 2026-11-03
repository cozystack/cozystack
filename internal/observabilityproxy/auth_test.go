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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeKube answers TokenReviews for the tokens in users and allows a
// SubjectAccessReview when allow returns true; it counts both calls.
type fakeKube struct {
	client   *fake.Clientset
	reviews  atomic.Int32
	sars     atomic.Int32
	lastSAR  atomic.Pointer[authorizationv1.SubjectAccessReviewSpec]
	reviewFn func() error
}

func newFakeKube(users map[string]string, allow func(user string, attrs *authorizationv1.ResourceAttributes) bool) *fakeKube {
	f := &fakeKube{client: fake.NewClientset()}
	f.client.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		f.reviews.Add(1)
		if f.reviewFn != nil {
			if err := f.reviewFn(); err != nil {
				return true, nil, err
			}
		}
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview).DeepCopy()
		if name, ok := users[tr.Spec.Token]; ok {
			tr.Status = authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: name, Groups: []string{"tenant-a-view"}}}
		}
		return true, tr, nil
	})
	f.client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		f.sars.Add(1)
		sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
		spec := sar.Spec
		f.lastSAR.Store(&spec)
		sar.Status.Allowed = allow(sar.Spec.User, sar.Spec.ResourceAttributes)
		return true, sar, nil
	})
	return f
}

func allowNamespace(ns string) func(string, *authorizationv1.ResourceAttributes) bool {
	return func(_ string, a *authorizationv1.ResourceAttributes) bool { return a != nil && a.Namespace == ns }
}

func TestAuthenticate(t *testing.T) {
	kube := newFakeKube(map[string]string{"good": "alice"}, allowNamespace("tenant-a"))
	a := NewAuthorizer(kube.client, 10*time.Second)
	ctx := context.Background()

	u, err := a.Authenticate(ctx, "good")
	if err != nil || u.Username != "alice" {
		t.Fatalf("Authenticate(good) = %+v, %v", u, err)
	}
	if _, err := a.Authenticate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	if n := kube.reviews.Load(); n != 1 {
		t.Fatalf("TokenReviews = %d, want 1 (second call cached)", n)
	}
	if _, err := a.Authenticate(ctx, "bad"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate(bad) err = %v, want ErrUnauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate(\"\") err = %v, want ErrUnauthenticated", err)
	}
}

func TestAuthenticateCacheExpires(t *testing.T) {
	kube := newFakeKube(map[string]string{"good": "alice"}, allowNamespace("tenant-a"))
	a := NewAuthorizer(kube.client, 10*time.Second)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := a.Authenticate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Second)
	if _, err := a.Authenticate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	if n := kube.reviews.Load(); n != 2 {
		t.Fatalf("TokenReviews = %d, want 2 after TTL", n)
	}
}

func TestAuthenticateErrorNotCached(t *testing.T) {
	kube := newFakeKube(map[string]string{"good": "alice"}, allowNamespace("tenant-a"))
	kube.reviewFn = func() error { return errors.New("apiserver down") }
	a := NewAuthorizer(kube.client, 10*time.Second)
	ctx := context.Background()
	if _, err := a.Authenticate(ctx, "good"); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	kube.reviewFn = nil
	if _, err := a.Authenticate(ctx, "good"); err != nil {
		t.Fatalf("err = %v after recovery", err)
	}
}

func TestAuthorize(t *testing.T) {
	kube := newFakeKube(map[string]string{"good": "alice"}, allowNamespace("tenant-a"))
	a := NewAuthorizer(kube.client, 10*time.Second)
	ctx := context.Background()
	user := &authenticationv1.UserInfo{Username: "alice", UID: "u1", Groups: []string{"g"}, Extra: map[string]authenticationv1.ExtraValue{"scope": {"x"}}}

	allowed := Route{Kind: NamespaceLogs, Namespace: "tenant-a"}
	denied := Route{Kind: NamespaceLogs, Namespace: "tenant-b"}

	ok, err := a.Authorize(ctx, user, allowed.ResourceAttributes())
	if err != nil || !ok {
		t.Fatalf("allow: %v, %v", ok, err)
	}
	spec := kube.lastSAR.Load()
	if spec.User != "alice" || spec.UID != "u1" || len(spec.Groups) != 1 || spec.Extra["scope"][0] != "x" {
		t.Fatalf("SAR identity not forwarded: %+v", spec)
	}
	if ra := spec.ResourceAttributes; ra.Verb != "list" || ra.Group != "cozystack.io" || ra.Resource != "logs" || ra.Namespace != "tenant-a" {
		t.Fatalf("SAR attributes = %+v", ra)
	}

	ok, err = a.Authorize(ctx, user, denied.ResourceAttributes())
	if err != nil || ok {
		t.Fatalf("deny: %v, %v", ok, err)
	}

	for range 3 {
		if ok, _ := a.Authorize(ctx, user, allowed.ResourceAttributes()); !ok {
			t.Fatal("cached allow flipped")
		}
		if ok, _ := a.Authorize(ctx, user, denied.ResourceAttributes()); ok {
			t.Fatal("cached deny flipped")
		}
	}
	if n := kube.sars.Load(); n != 2 {
		t.Fatalf("SubjectAccessReviews = %d, want 2 (rest cached)", n)
	}

	other := &authenticationv1.UserInfo{Username: "alice", UID: "u1", Groups: []string{"g", "tenant-b-view"}}
	if _, err := a.Authorize(ctx, other, denied.ResourceAttributes()); err != nil {
		t.Fatal(err)
	}
	if n := kube.sars.Load(); n != 3 {
		t.Fatalf("a different group set must not hit another identity's cache entry, SARs = %d", n)
	}
}
