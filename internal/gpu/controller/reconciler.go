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

// requestsKey runs a pass that only answers bind requests. A webhook waits
// for its answer for a few seconds, so it must not wait for moves, usage
// records and status of the whole cluster first.
var requestsKey = types.NamespacedName{Name: "gpu-binder-requests"}

const (
	// resync bounds how late a grace period is noticed when no event arrives.
	resync        = 30 * time.Second
	conflictRetry = 100 * time.Millisecond
)

// Reconciler is the GPU binder and the lifecycle of GPU classes, devices
// and their usage records. It runs in the leader only and is the single
// writer of a device's placement (internal/gpu/doc.go).
//
// Each pass re-reads every GPU object, which holds while clusters carry
// hundreds of devices; past that it wants per-object reconcilers.
type Reconciler struct {
	// Client reads through the cache and writes.
	Client client.Client
	// Reader reads GPUDevices past the cache.
	Reader client.Reader
	// Cache reads KubeVirt objects. The manager's client reads unstructured
	// objects from the API server, which would LIST every VMI on every
	// pass; nil means Client.
	Cache  client.Reader
	Config gpu.Config
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu sync.Mutex
	// attachedSince records when an attachment without a bind request was
	// first seen, to expire it after AttachmentGrace.
	attachedSince map[string]time.Time
	// degradedSince records when a node first served fewer units of a
	// resource than it holds.
	degradedSince map[string]time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile runs one pass: a full one for passKey, or one that answers only
// bind requests for requestsKey.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.mu.Lock()
	if r.attachedSince == nil {
		r.attachedSince = map[string]time.Time{}
		r.degradedSince = map[string]time.Time{}
	}
	r.mu.Unlock()

	w, err := r.load(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	var errs []error
	if req.NamespacedName == requestsKey {
		if r.Config.Enabled {
			errs = r.bindRequests(ctx, w)
		}
	} else {
		errs = r.pass(ctx, w)
	}
	if err := errors.Join(errs...); err != nil {
		if isConflict(errs) {
			// A webhook wrote a device between our read and our write; the
			// next pass sees it. A fixed delay, unlike an error, does not grow
			// a backoff on the one key every GPU decision shares.
			log.FromContext(ctx).V(1).Info("conflict, running again", "error", err)
			return ctrl.Result{RequeueAfter: conflictRetry}, nil
		}
		return ctrl.Result{}, err
	}
	if req.NamespacedName == passKey && (r.Config.Enabled || awaitingRelease(w)) {
		// Grace periods expire without an event, and with the feature
		// disabled nothing watches the VMI a deleted device waits for.
		return ctrl.Result{RequeueAfter: resync}, nil
	}
	return ctrl.Result{}, nil
}

func awaitingRelease(w *world) bool {
	for _, d := range w.devices {
		if !d.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(d, gpu.FinalizerDevice) {
			return true
		}
	}
	return false
}

// isConflict reports whether every error is a conflict. A joined error is
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
		if !apierrors.IsConflict(err) {
			return false
		}
	}
	return true
}

// pass applies, in order: finalizers and usage records, which run with the
// feature disabled too, then attachment lifecycle, bind requests, moves,
// Lost rebinds, new orders, and finally the derived state (class status,
// settings, metrics) over the placement this pass produced.
func (r *Reconciler) pass(ctx context.Context, w *world) []error {
	var errs []error
	errs = append(errs, r.ensureFinalizers(ctx, w)...)
	if r.Config.Enabled {
		// Attachments first: a VMI that ended detaches a Lost device, which
		// changes the set the device belongs to.
		errs = append(errs, r.attachments(ctx, w)...)
		errs = append(errs, r.normalizeSets(ctx, w)...)
	}
	if r.Config.Enabled && !w.aborted {
		errs = append(errs, r.bindRequests(ctx, w)...)
		errs = append(errs, r.relocate(ctx, w)...)
		errs = append(errs, r.rebindLost(ctx, w)...)
		errs = append(errs, r.bindOrders(ctx, w)...)
	}
	// Releasing a deleted device depends on its attachment, never on a
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

func (r *Reconciler) updateStatus(ctx context.Context, w *world, d *gpuv1alpha1.GPUDevice) error {
	if err := r.Client.Status().Update(ctx, d); err != nil {
		w.aborted = true
		return err
	}
	w.setDevice(d)
	return nil
}
