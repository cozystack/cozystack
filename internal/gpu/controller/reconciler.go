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

package controller

import (
	"context"
	"errors"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

// passKey is the only key the reconciler is ever enqueued with. Every
// event collapses into one pass over all GPU objects, so placement
// decisions are serialized without locks across objects.
var passKey = types.NamespacedName{Name: "gpu-binder"}

const (
	// resync bounds how late a grace period is noticed when no event arrives.
	resync        = 30 * time.Second
	conflictRetry = 100 * time.Millisecond
)

// Reconciler is the GPU binder and the lifecycle of GPU classes, claims
// and their usage records. It runs in the leader only and is the single
// writer of a claim's placement (internal/gpu/doc.go).
//
// Each pass re-reads every GPU object, which holds while clusters carry
// hundreds of claims; past that it wants per-object reconcilers.
type Reconciler struct {
	// Client reads through the cache and writes.
	Client client.Client
	// Reader reads GPUClaims, and pods whose gate decides a release, past
	// the cache.
	Reader client.Reader
	// Cache reads KubeVirt objects. The manager's client reads unstructured
	// objects from the API server, which would LIST every VMI on every
	// pass; nil means Client.
	Cache  client.Reader
	Config gpu.Config
	// Recorder reports on a VMI why its set waits; nil records nothing.
	Recorder events.EventRecorder
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu sync.Mutex
	// attachedSince records when an attachment whose VMI has not appeared
	// was first seen, to expire it after AttachmentGrace.
	attachedSince map[string]time.Time
	// degradedSince records when a node first served fewer units of a
	// resource than it holds.
	degradedSince map[string]time.Time
	// rejectedAs records each node as it was when a pinned pod was refused
	// there, so a change of the node ends the rejection before its until.
	rejectedAs map[string]nodeState
	// vmRejected keeps, per VirtualMachine, the nodes that refused a pinned
	// pod of its VMI, for its next VMI's template claims, which are new.
	// Kept in memory only: after a controller restart the next VMI may be
	// placed on such a node once more. Held claims of the set keep their
	// rejections in status.rejectedNodes, which a restart does not lose.
	vmRejected map[types.NamespacedName][]gpuv1alpha1.RejectedNode
	// waitReason is the reason last reported on each VMI whose set waits.
	waitReason map[types.UID]string
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	r.mu.Lock()
	if r.attachedSince == nil {
		r.attachedSince = map[string]time.Time{}
	}
	if r.degradedSince == nil {
		r.degradedSince = map[string]time.Time{}
	}
	if r.rejectedAs == nil {
		r.rejectedAs = map[string]nodeState{}
	}
	if r.vmRejected == nil {
		r.vmRejected = map[types.NamespacedName][]gpuv1alpha1.RejectedNode{}
	}
	if r.waitReason == nil {
		r.waitReason = map[types.UID]string{}
	}
	r.mu.Unlock()

	w, err := r.load(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	errs := r.pass(ctx, w)
	if err := errors.Join(errs...); err != nil {
		if isConflict(errs) {
			// Someone wrote an object between our read and our write; the
			// next pass sees it. A fixed delay, unlike an error, does not grow
			// a backoff on the one key every GPU decision shares.
			log.FromContext(ctx).V(1).Info("conflict, running again", "error", err)
			return ctrl.Result{RequeueAfter: conflictRetry}, nil
		}
		return ctrl.Result{}, err
	}
	if r.Config.Enabled || awaitingRelease(w) {
		// Grace periods expire without an event, and with the feature
		// disabled nothing watches the VMI a deleted claim waits for.
		return ctrl.Result{RequeueAfter: resync}, nil
	}
	return ctrl.Result{}, nil
}

func awaitingRelease(w *world) bool {
	for _, d := range w.claims {
		if !d.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(d, gpu.FinalizerClaim) {
			return true
		}
	}
	return false
}

// isConflict reports whether every error is a conflict, or a create of an
// object the cache had not shown yet. A joined error is
// judged by its parts: apierrors.IsConflict finds a conflict anywhere in a
// join and would hide another error next to it.
func isConflict(errs []error) bool {
	for _, err := range errs {
		if err == nil {
			continue
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			if !isConflict(joined.Unwrap()) {
				return false
			}
			continue
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return false
		}
	}
	return true
}

// pass applies, in order: finalizers and usage records, which run with the
// feature disabled too, then attachment lifecycle, the placement of gated
// launcher pods' sets, the recovery of pinned pods their node refuses,
// moves, Lost rebinds, new orders, and finally the derived state (class
// status, settings, metrics) over the placement this pass produced. Gated
// pods go before moves and orders: their VMs start now.
func (r *Reconciler) pass(ctx context.Context, w *world) []error {
	var errs []error
	errs = append(errs, r.ensureFinalizers(ctx, w)...)
	if r.Config.Enabled {
		// Attachments first: a VMI that ended detaches a Lost claim, which
		// changes the set the claim belongs to.
		errs = append(errs, r.attachments(ctx, w)...)
		errs = append(errs, r.normalizeSets(ctx, w)...)
	}
	if r.Config.Enabled && !w.aborted {
		errs = append(errs, r.pruneRejections(ctx, w)...)
		errs = append(errs, r.gates(ctx, w)...)
		errs = append(errs, r.recoverRefused(ctx, w)...)
		errs = append(errs, r.relocate(ctx, w)...)
		errs = append(errs, r.rebindLost(ctx, w)...)
		errs = append(errs, r.bindOrders(ctx, w)...)
	}
	// Releasing a deleted claim depends on its attachment, never on a
	// placement, so it runs even when a placement write failed.
	errs = append(errs, r.releaseDeleted(ctx, w)...)
	if w.aborted {
		// Statuses in memory may hold a placement that was never written;
		// records follow the next pass's fresh read.
		return errs
	}
	errs = append(errs, r.usageRecords(ctx, w)...)
	if r.Config.Enabled {
		errs = append(errs, r.classStatus(ctx, w)...)
		errs = append(errs, r.nodeStatus(ctx, w)...)
		errs = append(errs, r.settings(ctx, w))
		r.exportMetrics(w)
	}
	return errs
}

func (r *Reconciler) updateStatus(ctx context.Context, w *world, d *gpuv1alpha1.GPUClaim) error {
	if err := r.Client.Status().Update(ctx, d); err != nil {
		w.aborted = true
		return err
	}
	w.setClaim(d)
	return nil
}
