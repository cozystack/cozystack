// SPDX-License-Identifier: Apache-2.0

package fluxplunger

import (
	"context"
	"errors"
	"fmt"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// drainPollInterval is how often a drain re-reads the releases it still holds.
	drainPollInterval = time.Second
	// ShutdownDrainTimeoutFlag names the flag that bounds the drain; the chart
	// passes it, derived from the pod's termination grace period.
	ShutdownDrainTimeoutFlag = "shutdown-drain-timeout"
)

// DrainFences gives up every fence flux-plunger holds, for a flux-plunger that is
// stopping. A live release has its recovery aborted, the suspension and the fence
// lifted together, which leaves it as recovery found it for the next flux-plunger
// to pick up. A release being deleted goes through reconcileTerminating until the
// fence is gone: flux-plunger's suspension is lifted, the fence is kept until
// helm-controller has handled the unsuspended generation, and only then lifted.
// When ctx ends first, the fences still held are left in place and counted: the
// next flux-plunger carries on with them, and when flux-plunger is being removed
// the chart's pre-delete hook releases them. Reads go to the apiserver, since the
// informer cache has stopped with the manager by now.
func (r *FluxPlunger) DrainFences(ctx context.Context) (int, error) {
	logger := log.FromContext(ctx)
	var reader client.Reader = r.Client
	if r.APIReader != nil {
		reader = r.APIReader
	}
	// held is what the last listing that succeeded found; -1 until one has.
	held := -1
	for {
		list := &helmv2.HelmReleaseList{}
		if err := reader.List(ctx, list); err != nil {
			// A failed listing is retried like any other step: on the uninstall
			// path ending here would leave the pre-delete hook to let go of a
			// release the drain could still have carried through its uninstall.
			logger.Info("Could not list HelmReleases yet, retrying", "error", err.Error())
			select {
			case <-ctx.Done():
				return held, ctx.Err()
			case <-time.After(drainPollInterval):
			}
			continue
		}
		held = 0
		for i := range list.Items {
			hr := &list.Items[i]
			if !controllerutil.ContainsFinalizer(hr, fenceFinalizer) {
				continue
			}
			held++
			var err error
			if hr.DeletionTimestamp.IsZero() {
				// The relinquishing apply drops every field flux-plunger's manager owns,
				// so a suspension of its own goes with the fence, and one it does not own
				// is left alone.
				err = r.releaseFence(ctx, hr)
			} else {
				_, err = r.reconcileTerminating(ctx, hr)
			}
			if err != nil {
				logger.Info("Could not give up a fence yet, retrying", "helmrelease", client.ObjectKeyFromObject(hr).String(), "error", err.Error())
			}
		}
		if held == 0 {
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return held, ctx.Err()
		case <-time.After(drainPollInterval):
		}
	}
}

const (
	// managerStopTimeout bounds how long the manager may take to stop its
	// controllers before the drain starts.
	managerStopTimeout = 10 * time.Second
	// killMargin is left between the end of the drain and the kubelet's SIGKILL.
	// The chart's --shutdown-drain-timeout is the grace period less this and
	// managerStopTimeout, which TestDeploymentTemplate_SubtractsTheStopAndTheMarginTheBinaryUses
	// holds.
	killMargin = 15 * time.Second
	// leaseRetryPeriod is how often the leader renews its Lease.
	leaseRetryPeriod = 5 * time.Second
	// leaseRenewDeadline is how long a leader whose renewals fail goes on acting
	// before it gives up; it is part of what the Lease has to outlast.
	leaseRenewDeadline = 15 * time.Second
	// leaseMargin is added on top of everything a stop can take.
	leaseMargin = 20 * time.Second
)

// ApplyShutdownTimings sizes the leader Lease so the next flux-plunger cannot
// lead while this one drains. A rollout or an eviction starts the next pod while
// the old one is still terminating, whatever the Deployment strategy, so it is
// the Lease, not the rollout, that keeps a single writer. The Lease is not given
// up on stop, and it outlasts its last renewal, the renew deadline, the
// manager's bounded stop and the drain; the price is that a new pod waits for it
// to run out before it leads.
func ApplyShutdownTimings(opts *ctrl.Options, drain time.Duration) {
	stop := managerStopTimeout
	renew := leaseRenewDeadline
	lease := leaseRetryPeriod + renew + stop + drain + leaseMargin
	retry := leaseRetryPeriod
	opts.GracefulShutdownTimeout = &stop
	opts.LeaseDuration = &lease
	opts.RenewDeadline = &renew
	opts.RetryPeriod = &retry
	opts.LeaderElectionReleaseOnCancel = false
}

// drainingManager is the part of a ctrl.Manager RunManager uses.
type drainingManager interface {
	Start(context.Context) error
	GetClient() client.Client
	GetAPIReader() client.Reader
	Elected() <-chan struct{}
}

// RunManager runs the manager, then drains flux-plunger's fences. The drain
// reads through the manager's API reader: once the manager has stopped, its
// informer cache is frozen and would show fences already gone. Only a
// flux-plunger that led drains: one that never did stops while the leader goes
// on, and the fences are the leader's. Without leader election Elected is closed
// from the start.
func RunManager(ctx context.Context, mgr drainingManager, drain time.Duration) error {
	drainer := &FluxPlunger{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Recorder:  apiEventRecorder{c: mgr.GetClient()},
	}
	return RunThenDrain(ctx, mgr.Start, func(dctx context.Context) (int, error) {
		select {
		case <-mgr.Elected():
			return drainer.DrainFences(dctx)
		default:
			log.FromContext(dctx).Info("Never became leader; leaving the fences to the leader")
			return 0, nil
		}
	}, drain)
}

// RunThenDrain runs the manager until it stops, then drains under a deadline of
// its own: the context run was given is cancelled by the stop signal, so the drain
// cannot be bounded by it. A manager that failed is not drained: a lost Lease
// means another flux-plunger leads now. A stop that overran the manager's grace
// period is drained anyway, since a reconcile slow to notice its cancelled
// context must not cost the fences their drain, and is still reported. A drain
// that leaves fences behind is logged rather than turned into a failed exit,
// since the next flux-plunger or the pre-delete hook takes them over.
func RunThenDrain(ctx context.Context, run func(context.Context) error, drain func(context.Context) (int, error), timeout time.Duration) error {
	runErr := run(ctx)
	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	dctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	logger := log.FromContext(ctx)
	if left, err := drain(dctx); err != nil {
		logger.Info("Stopped with uninstall fences still held; the next flux-plunger or the chart's pre-delete hook releases them", "held", left, "error", err.Error())
	} else {
		logger.Info("Released every uninstall fence before stopping")
	}
	return runErr
}

// apiEventRecorder writes each Event straight to the apiserver. The drain cannot
// use the manager's recorder: the manager stops its recorder provider before
// Start returns, after which that recorder delivers nothing.
type apiEventRecorder struct {
	c client.Client
}

func (r apiEventRecorder) Event(obj runtime.Object, eventtype, reason, message string) {
	r.write(obj, eventtype, reason, message)
}

func (r apiEventRecorder) Eventf(obj runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	r.write(obj, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

func (r apiEventRecorder) AnnotatedEventf(obj runtime.Object, _ map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	r.write(obj, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

func (r apiEventRecorder) write(obj runtime.Object, eventtype, reason, message string) {
	logger := log.Log.WithName("drain-events")
	o, ok := obj.(client.Object)
	if !ok {
		logger.Info("Cannot record an event for this object", "type", fmt.Sprintf("%T", obj), "reason", reason)
		return
	}
	gvk, err := apiutil.GVKForObject(o, r.c.Scheme())
	if err != nil {
		logger.Info("Cannot record an event", "reason", reason, "error", err.Error())
		return
	}
	now := metav1.Now()
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: o.GetName() + ".", Namespace: o.GetNamespace()},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
			UID:        o.GetUID(),
		},
		Reason:         reason,
		Message:        message,
		Type:           eventtype,
		Source:         corev1.EventSource{Component: suspendFieldManager},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.c.Create(ctx, ev); err != nil {
		logger.Info("Could not record an event", "reason", reason, "error", err.Error())
	}
}
