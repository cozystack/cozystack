/*
Copyright 2025 The Cozystack Authors.

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

package manifestutil

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCollectCRDNames(t *testing.T) {
	objects := []*unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": "test-ns"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "test-deploy"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "packagesources.cozystack.io"},
		}},
	}

	names := CollectCRDNames(objects)
	if len(names) != 2 {
		t.Fatalf("CollectCRDNames() returned %d names, want 2", len(names))
	}
	if names[0] != "packages.cozystack.io" {
		t.Errorf("names[0] = %q, want %q", names[0], "packages.cozystack.io")
	}
	if names[1] != "packagesources.cozystack.io" {
		t.Errorf("names[1] = %q, want %q", names[1], "packagesources.cozystack.io")
	}
}

func TestCollectCRDNames_ignoresWrongAPIVersion(t *testing.T) {
	objects := []*unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "real.crd.io"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1beta1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "legacy.crd.io"},
		}},
	}

	names := CollectCRDNames(objects)
	if len(names) != 1 {
		t.Fatalf("CollectCRDNames() returned %d names, want 1", len(names))
	}
	if names[0] != "real.crd.io" {
		t.Errorf("names[0] = %q, want %q", names[0], "real.crd.io")
	}
}

func TestCollectCRDNames_noCRDs(t *testing.T) {
	objects := []*unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": "test"},
		}},
	}

	names := CollectCRDNames(objects)
	if len(names) != 0 {
		t.Errorf("CollectCRDNames() returned %d names, want 0", len(names))
	}
}

func TestWaitForCRDsEstablished_success(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// Create a CRD object in the fake client
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
	}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crd).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return nil
				}
				if u.GetKind() == "CustomResourceDefinition" {
					_ = unstructured.SetNestedSlice(u.Object, []interface{}{
						map[string]interface{}{
							"type":   "Established",
							"status": "True",
						},
					}, "status", "conditions")
				}
				return nil
			},
		}).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"packages.cozystack.io"})
	if err != nil {
		t.Fatalf("WaitForCRDsEstablished() error = %v", err)
	}
}

func TestWaitForCRDsEstablished_timeout(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// CRD exists but never gets Established condition
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
	}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crd).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"packages.cozystack.io"})
	if err == nil {
		t.Fatal("WaitForCRDsEstablished() expected error on timeout, got nil")
	}
	if !strings.Contains(err.Error(), "packages.cozystack.io") {
		t.Errorf("error should mention stuck CRD name, got: %v", err)
	}
	// The only deadline-driven test here, so it is the only one that can catch
	// the cause being reported as something other than what ended the wait.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap the deadline cause, got: %v", err)
	}
}

// crdObject builds a CustomResourceDefinition for the fake client. Passing nil
// conditions leaves status unset, which is one of the two shapes the wait reads
// as "not established yet"; the other is a status carrying conditions that do
// not include an established one.
func crdObject(name string, conditions []interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": name},
	}
	if conditions != nil {
		obj["status"] = map[string]interface{}{"conditions": conditions}
	}
	return &unstructured.Unstructured{Object: obj}
}

func establishedConditions() []interface{} {
	return []interface{}{map[string]interface{}{"type": "Established", "status": "True"}}
}

func unestablishedConditions() []interface{} {
	return []interface{}{map[string]interface{}{"type": "NamesAccepted", "status": "True"}}
}

// cancellationScheme returns the scheme the cancellation tests share.
func cancellationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}
	return scheme
}

// assertCancelled checks the whole message, not just the CRD in it. The point
// of the fix is that every way out of the wait reports cancellation the same
// way, so a second message that happens to carry a name is still the defect;
// comparing the text is what makes that observable to a test.
func assertCancelled(t *testing.T, err error, wantCRD string) {
	t.Helper()
	if err == nil {
		t.Fatal("WaitForCRDsEstablished() expected error on cancelled context, got nil")
	}
	want := fmt.Sprintf("context cancelled while waiting for CRD %q to be established: %v", wantCRD, context.Canceled)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	// Callers wrap this error again, so the cause has to survive for anyone
	// matching on it rather than on the text.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap the context cause, got: %v", err)
	}
}

// TestWaitForCRDsEstablished_cancelledBeforeFirstPoll covers the interleaving
// the timeout test above cannot reach: the context is already cancelled when
// the function is entered, so no poll ever observes the set and the name in
// the message can only be the one the wait starts with. The client answers
// regardless of the context, so a wait that polled anyway would settle on the
// second CRD and be visible here rather than passing by accident.
func TestWaitForCRDsEstablished_cancelledBeforeFirstPoll(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	fakeClient := fake.NewClientBuilder().
		WithScheme(cancellationScheme(t)).
		WithObjects(
			crdObject("established.cozystack.io", establishedConditions()),
			crdObject("packages.cozystack.io", nil),
		).
		Build()

	ctx, cancel := context.WithCancel(context.Background())
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))
	cancel()

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"established.cozystack.io", "packages.cozystack.io"})
	assertCancelled(t, err, "established.cozystack.io")
}

// TestWaitForCRDsEstablished_cancelledMidPollNamesUncheckedCRD pins which CRD
// the message names when the deadline lands inside a poll. The poll walks past
// one CRD it confirms established and is cut off on the next, which is the one
// whose state is genuinely unknown and the one worth reporting. A third name
// behind it keeps "the first Get to fail" apart from "the last one tried",
// which two CRDs cannot tell apart.
func TestWaitForCRDsEstablished_cancelledMidPollNamesUncheckedCRD(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	// The first Get is served, so the first CRD is confirmed established. The
	// deadline then lands on the second, the way it would against a live
	// client: cancelled first, refused second.
	gets := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(cancellationScheme(t)).
		WithObjects(
			crdObject("established.cozystack.io", establishedConditions()),
			crdObject("packages.cozystack.io", nil),
			crdObject("later.cozystack.io", nil),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				gets++
				if gets > 1 {
					cancel()
					return context.Canceled
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"established.cozystack.io", "packages.cozystack.io", "later.cozystack.io"})
	assertCancelled(t, err, "packages.cozystack.io")
}

// TestWaitForCRDsEstablished_cancelledAfterPollKeepsName pins that the name a
// completed poll settled on survives the wait that follows it. The two cases
// are the two shapes of "not established yet", which the loop records from
// different branches.
func TestWaitForCRDsEstablished_cancelledAfterPollKeepsName(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	for _, tc := range []struct {
		name       string
		conditions []interface{}
	}{
		{name: "status absent", conditions: nil},
		{name: "no established condition", conditions: unestablishedConditions()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

			// Both Gets are served, so the poll completes and settles on the
			// second CRD. The context then dies while the loop waits for a tick.
			gets := 0
			fakeClient := fake.NewClientBuilder().
				WithScheme(cancellationScheme(t)).
				WithObjects(
					crdObject("established.cozystack.io", establishedConditions()),
					crdObject("packages.cozystack.io", tc.conditions),
				).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						err := c.Get(ctx, key, obj, opts...)
						gets++
						if gets == 2 {
							cancel()
						}
						return err
					},
				}).
				Build()

			err := WaitForCRDsEstablished(ctx, fakeClient, []string{"established.cozystack.io", "packages.cozystack.io"})
			assertCancelled(t, err, "packages.cozystack.io")
		})
	}
}

// reenteringContext forces the loop to start an iteration on a context that is
// already cancelled. Against a real context that needs the bottom select to
// have taken the tick while both cases were ready, which is a coin toss no test
// can hold still. Done stays open for the single read that select makes in the
// iteration where cancellation lands, so the tick wins by construction; every
// read after that returns a closed channel, so the next iteration sees a
// context cancelled through both Done and Err, the way a real one is. The
// standard library guarantees that pairing rather than merely happening to
// provide it: cancelCtx.Err reads <-c.Done() before returning a non-nil error.
type reenteringContext struct {
	context.Context
	dead    *atomic.Bool
	tickWon *atomic.Bool
	open    chan struct{}
	closed  chan struct{}
}

func newReenteringContext(parent context.Context) reenteringContext {
	closed := make(chan struct{})
	close(closed)
	return reenteringContext{
		Context: parent,
		dead:    &atomic.Bool{},
		tickWon: &atomic.Bool{},
		open:    make(chan struct{}),
		closed:  closed,
	}
}

func (c reenteringContext) Done() <-chan struct{} {
	if c.dead.Load() && !c.tickWon.CompareAndSwap(false, true) {
		return c.closed
	}
	return c.open
}

func (c reenteringContext) Err() error {
	if c.dead.Load() && c.tickWon.Load() {
		return context.Canceled
	}
	return nil
}

// TestWaitForCRDsEstablished_cancelledOnLoopReentryKeepsName pins that the CRD
// name survives into the next iteration. One poll completes and settles on the
// second CRD, the context dies during the wait, and the loop re-enters and
// leaves through the check at the top. That check has no poll of its own to
// take a name from, so the one it reports has to be the one carried over.
func TestWaitForCRDsEstablished_cancelledOnLoopReentryKeepsName(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	base := log.IntoContext(context.Background(), log.FromContext(context.Background()))
	reCtx := newReenteringContext(base)

	gets := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(cancellationScheme(t)).
		WithObjects(
			crdObject("established.cozystack.io", establishedConditions()),
			crdObject("packages.cozystack.io", nil),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				err := c.Get(ctx, key, obj, opts...)
				gets++
				if gets == 2 {
					reCtx.dead.Store(true)
				}
				return err
			},
		}).
		Build()

	// The double answers Done from a fixed script, so a loop that stops reading
	// it the expected number of times never leaves. Bound the call: a hang here
	// costs the whole package's CI timeout, a failure costs one line.
	done := make(chan error, 1)
	go func() {
		done <- WaitForCRDsEstablished(reCtx, fakeClient, []string{"established.cozystack.io", "packages.cozystack.io"})
	}()

	select {
	case err := <-done:
		assertCancelled(t, err, "packages.cozystack.io")
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForCRDsEstablished() never returned: the loop stopped taking the cancellation exits this fixture scripts")
	}
}

func TestWaitForCRDsEstablished_empty(t *testing.T) {
	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx := context.Background()
	err := WaitForCRDsEstablished(ctx, fakeClient, nil)
	if err != nil {
		t.Fatalf("WaitForCRDsEstablished() with empty names should return nil, got: %v", err)
	}
}
