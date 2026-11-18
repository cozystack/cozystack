// SPDX-License-Identifier: Apache-2.0

// Package applock serializes an application's credential operations across the
// API server's replicas. A create or update seeds and prunes account Secrets
// outside the HelmRelease write's optimistic concurrency, so two concurrent
// writes to one application can interleave: one deletes a Secret the other has
// just reused and referenced. The lock makes those operations take turns, keyed
// by the application, through a coordination.k8s.io Lease.
package applock

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrHeld means another operation holds the application's lock. The caller
// turns it into a retryable Conflict rather than waiting, so a stuck holder
// cannot block a write past its lease.
var ErrHeld = errors.New("another change to the application's credentials is in progress")

// Locker takes and releases the per-application lock.
type Locker struct {
	// c reads and writes Leases; it must be uncached so an acquire sees a Lease
	// a concurrent operation wrote a moment ago.
	c client.Client
	// namespace holds the Leases; cozy-system, which tenants cannot write, so a
	// tenant cannot hold or delete its own application's lock.
	namespace string
	// duration is how long a Lease is honored without renewal. An operation
	// finishes well within it; a holder that crashed has its Lease stolen once
	// it expires, so the lock never wedges.
	duration time.Duration
}

// New returns a Locker that keeps its Leases in namespace (cozy-system).
func New(c client.Client, namespace string) *Locker {
	return &Locker{c: c, namespace: namespace, duration: 60 * time.Second}
}

// leaseName is the Lease name for one application, a hash so any namespace and
// release name map to a valid, collision-free object name.
func leaseName(appNamespace, release string) string {
	sum := sha256.Sum256([]byte(appNamespace + "/" + release))
	return "cred-" + hex.EncodeToString(sum[:])[:40]
}

func holderID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:])), nil
}

// Acquire takes the application's lock. On success it returns a release
// function the caller defers. It returns ErrHeld when another operation holds a
// live lock. Any other error is an infrastructure failure the caller may treat
// as fail-open, since the lock only guards a rare race and the Lease store being
// unreachable is no worse than not having the lock at all.
func (l *Locker) Acquire(ctx context.Context, appNamespace, release string) (func(), error) {
	name := leaseName(appNamespace, release)
	id, err := holderID()
	if err != nil {
		return nil, err
	}
	now := metav1.NewMicroTime(time.Now())
	dur := int32(l.duration / time.Second)

	cur := &coordinationv1.Lease{}
	err = l.c.Get(ctx, client.ObjectKey{Namespace: l.namespace, Name: name}, cur)
	switch {
	case apierrors.IsNotFound(err):
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Namespace: l.namespace, Name: name},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &id,
				LeaseDurationSeconds: &dur,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := l.c.Create(ctx, lease); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, ErrHeld // another operation created it between the Get and here
			}
			return nil, err
		}
		return l.releaser(name, id), nil
	case err != nil:
		return nil, err
	}

	// A Lease exists: steal it only if it has expired, under its resourceVersion
	// so two stealers do not both win.
	if held(cur) {
		return nil, ErrHeld
	}
	cur.Spec.HolderIdentity = &id
	cur.Spec.LeaseDurationSeconds = &dur
	cur.Spec.AcquireTime = &now
	cur.Spec.RenewTime = &now
	if err := l.c.Update(ctx, cur); err != nil {
		if apierrors.IsConflict(err) {
			return nil, ErrHeld // another operation stole or renewed it first
		}
		return nil, err
	}
	return l.releaser(name, id), nil
}

// held reports whether a Lease is still honored: it has a holder and its renew
// time plus its duration is in the future.
func held(lease *coordinationv1.Lease) bool {
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" || lease.Spec.RenewTime == nil {
		return false
	}
	dur := time.Duration(60) * time.Second
	if lease.Spec.LeaseDurationSeconds != nil {
		dur = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	return lease.Spec.RenewTime.Add(dur).After(time.Now())
}

// releaser deletes the Lease if this operation still holds it. Best effort: a
// failed release leaves a Lease that the next acquire steals once it expires.
func (l *Locker) releaser(name, id string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cur := &coordinationv1.Lease{}
		if err := l.c.Get(ctx, client.ObjectKey{Namespace: l.namespace, Name: name}, cur); err != nil {
			return
		}
		if cur.Spec.HolderIdentity == nil || *cur.Spec.HolderIdentity != id {
			return // someone else holds it now; not ours to delete
		}
		_ = l.c.Delete(ctx, cur, client.Preconditions{UID: &cur.UID, ResourceVersion: &cur.ResourceVersion})
	}
}
