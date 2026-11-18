// SPDX-License-Identifier: Apache-2.0

package applock

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newClient(t *testing.T) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).Build()
}

func TestAcquireBlocksAndReleases(t *testing.T) {
	ctx := context.Background()
	l := New(newClient(t), "cozy-system")

	unlock, err := l.Acquire(ctx, "tenant-foo", "postgres-orders")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Held now: a second acquire of the same application is refused.
	if _, err := l.Acquire(ctx, "tenant-foo", "postgres-orders"); !errors.Is(err, ErrHeld) {
		t.Fatalf("a held lock must refuse, got %v", err)
	}
	// A different application locks independently.
	if u2, err := l.Acquire(ctx, "tenant-foo", "postgres-other"); err != nil {
		t.Fatalf("a different application must lock independently: %v", err)
	} else {
		u2()
	}
	// After release, the application can be locked again.
	unlock()
	if u3, err := l.Acquire(ctx, "tenant-foo", "postgres-orders"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		u3()
	}
}

func seedLease(t *testing.T, c client.Client, appNamespace, release, holder string, renew time.Time) {
	t.Helper()
	rt := metav1.NewMicroTime(renew)
	dur := int32(60)
	if err := c.Create(context.Background(), &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cozy-system", Name: leaseName(appNamespace, release)},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &dur, RenewTime: &rt},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRefusesAFreshLeaseOfAnother(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	seedLease(t, c, "tenant-foo", "postgres-orders", "someone-else", time.Now())
	if _, err := New(c, "cozy-system").Acquire(ctx, "tenant-foo", "postgres-orders"); !errors.Is(err, ErrHeld) {
		t.Fatalf("a fresh lease held by another must refuse, got %v", err)
	}
}

func TestAcquireStealsAnExpiredLease(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	// Held by a holder that last renewed longer than a lease duration ago.
	seedLease(t, c, "tenant-foo", "postgres-orders", "crashed-holder", time.Now().Add(-5*time.Minute))
	unlock, err := New(c, "cozy-system").Acquire(ctx, "tenant-foo", "postgres-orders")
	if err != nil {
		t.Fatalf("an expired lease must be stealable: %v", err)
	}
	unlock()
}
