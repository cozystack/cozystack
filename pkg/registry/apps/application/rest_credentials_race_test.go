// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

type roleKey struct{}

func ctxRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

// TestConcurrentCreateLoserSeededLiveSecret exercises the mirror image of
// TestConcurrentCreateKeepsLiveSecret: the create that LOSES the HelmRelease
// race is the one that seeded the Secret which becomes live, and the WINNER
// adopts that very Secret. The loser's recorded UID therefore equals the live
// Secret's UID, so a guard that keyed on Secret identity alone would let the
// loser delete the live release's account Secret; unseed must skip because the
// release exists.
//
// Global schedule enforced by the gates below:
//  1. W seeds its account Secrets (S_W).
//  2. L passes the "HR absent" check, replaces S_W with S_L (its own UID).
//  3. W creates the HelmRelease (wins) and adopts S_L.
//  4. L's HelmRelease create fails AlreadyExists; L.unseed runs.
//  5. L.unseed finds the release live, so it deletes nothing.
func TestConcurrentCreateLoserSeededLiveSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Resource{
		Application: config.ApplicationConfig{Kind: postgresKind, Singular: "postgres", Plural: "postgreses", OpenAPISchema: credentialsSchema},
		Release:     config.ReleaseConfig{Prefix: "postgres-"},
		Credentials: credentialsBlock(),
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{*cfg}}); err != nil {
		t.Fatal(err)
	}

	wSeeded := make(chan struct{})
	lSeeded := make(chan struct{})
	wHRDone := make(chan struct{})

	var uids atomic.Int64
	assign := func(obj client.Object) {
		if obj.GetUID() == "" {
			obj.SetUID(types.UID(fmt.Sprintf("uid-%d", uids.Add(1))))
		}
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			role, _ := ctx.Value(roleKey{}).(string)
			if _, isHR := obj.(*helmv2.HelmRelease); isHR {
				switch role {
				case "W":
					close(wSeeded) // W has finished seeding S_W
					<-lSeeded      // wait until L has replaced S_W with S_L
					assign(obj)
					err := cl.Create(ctx, obj, opts...)
					close(wHRDone) // W has won the release
					return err
				case "L":
					close(lSeeded) // L has finished its replace
					<-wHRDone      // wait until W owns the release
					assign(obj)
					return cl.Create(ctx, obj, opts...) // AlreadyExists
				}
			}
			assign(obj)
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetUID() == "" {
				cur := obj.DeepCopyObject().(client.Object)
				if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), cur); err == nil {
					obj.SetUID(cur.GetUID())
				}
			}
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	r := NewREST(c, c, cfg)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // winner
		defer wg.Done()
		if _, err := r.Create(ctxRole(credentialsCtx(), "W"), postgresApp(`{"users":{"web":{}}}`), nil, &metav1.CreateOptions{}); err != nil {
			t.Errorf("winner create: %v", err)
		}
	}()

	<-wSeeded // W seeded before L starts, so L's Ensure sees W's Secret as the leftover to replace

	go func() { // loser
		defer wg.Done()
		_, err := r.Create(ctxRole(credentialsCtx(), "L"), postgresApp(`{"users":{"web":{}}}`), nil, &metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			t.Errorf("loser create: %v, want AlreadyExists", err)
		}
	}()

	wg.Wait()

	hr := getRelease(t, c)
	sec := accountSecrets(t, c)["postgres-orders.web.account"]
	if sec == nil {
		t.Fatalf("the live release %s lost its account Secret: the losing create deleted the Secret it had seeded and the winner adopted", hr.Name)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != hr.UID {
		t.Fatalf("surviving Secret owners = %v, want the live release", sec.OwnerReferences)
	}
}
