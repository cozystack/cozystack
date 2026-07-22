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

package dbautoscaler

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// requeueInterval keeps metric-derived values fresh, mirroring the WorkloadMonitor
// controller's cadence.
const requeueInterval = 60 * time.Second

// defaults for behavior knobs the tenant may omit.
const (
	defaultScaleUpWindow       = 300 * time.Second
	defaultScaleDownWindow     = 1800 * time.Second
	defaultStep                = int32(1)
	defaultConvergenceDeadline = 900 * time.Second
	defaultMaxReplicationLag   = int32(30)
)

// targetState is the per-DHA in-memory state. A single active reconciler
// (leader-elected) owns it, so no cross-replica coordination is needed.
type targetState struct {
	history       []Recommendation
	inFlightSince *time.Time
	// lastWritten is the replica count the autoscaler last wrote, used to detect
	// a competing writer that changed replicas out from under us.
	lastWritten   *int32
	lastConverged *int32
	// failedTarget is a scale-up size that did not converge and was rolled back;
	// re-attempting it is deferred until backoffUntil (exponential per repeat) so
	// a persistently unschedulable target does not thrash patch->stuck->rollback.
	failedTarget *int32
	backoffUntil *time.Time
	backoffCount int
}

// Reconciler reconciles DatabaseHorizontalAutoscaler objects.
type Reconciler struct {
	client.Client
	dynamic.Interface
	apimeta.RESTMapper
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	VM       *VMClient

	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time

	// BaseURLFor resolves the vmselect base URL for a namespace. Injectable for
	// tests; defaults to the namespace monitoring-label resolver.
	BaseURLFor func(ctx context.Context, namespace string) string

	mu    sync.Mutex
	state map[types.NamespacedName]*targetState
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) stateFor(key types.NamespacedName) *targetState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		r.state = map[types.NamespacedName]*targetState{}
	}
	s := r.state[key]
	if s == nil {
		s = &targetState{}
		r.state[key] = s
	}
	return s
}

func (r *Reconciler) dropState(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, key)
}

// +kubebuilder:rbac:groups=autoscaling.cozystack.io,resources=databasehorizontalautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=autoscaling.cozystack.io,resources=databasehorizontalautoscalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.cozystack.io,resources=*,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=resourcequotas;namespaces;pods,verbs=get;list;watch

// Reconcile runs one decision cycle for a DHA.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	dha := &autoscalingv1alpha1.DatabaseHorizontalAutoscaler{}
	if err := r.Get(ctx, req.NamespacedName, dha); err != nil {
		if apierrors.IsNotFound(err) {
			r.dropState(req.NamespacedName)
			clearMetrics(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	adapter := AdapterFor(dha.Spec.TargetRef.Kind)
	if adapter == nil {
		r.setScalingActive(dha, false, autoscalingv1alpha1.ReasonSharded,
			fmt.Sprintf("kind %q is not horizontally scalable", dha.Spec.TargetRef.Kind))
		return r.finish(ctx, dha, ctrl.Result{})
	}

	appValues, currentReplicas, appAnnotations, mapping, err := r.loadApplication(ctx, dha, adapter)
	if err != nil {
		logger.Error(err, "load application")
		r.setScalingActive(dha, false, autoscalingv1alpha1.ReasonNotScalable, err.Error())
		return r.finish(ctx, dha, ctrl.Result{RequeueAfter: requeueInterval})
	}

	if ok, reason := adapter.Scalable(appValues); !ok {
		r.setScalingActive(dha, false, autoscalingv1alpha1.ReasonNotScalable, reason)
		return r.finish(ctx, dha, ctrl.Result{})
	}
	r.setScalingActive(dha, true, autoscalingv1alpha1.ReasonReady, "target is scalable")

	st := r.stateFor(req.NamespacedName)

	// Reconstruct the last-written count from persisted status after a restart or
	// leader failover, so the ownership back-off survives (the in-memory state map
	// starts empty on a new leader).
	if st.lastWritten == nil && dha.Status.LastAppliedReplicas != nil {
		w := *dha.Status.LastAppliedReplicas
		st.lastWritten = &w
	}

	// WorkloadMonitor: operational + convergence signal.
	operational, availableReplicas := r.loadWorkloadMonitor(ctx, dha.Namespace, adapter.ReleaseName(dha.Spec.TargetRef.Name), currentReplicas)
	scaleInFlight := availableReplicas != currentReplicas

	// Track convergence: once converged, clear single-flight and record the count.
	if operational && !scaleInFlight {
		st.inFlightSince = nil
		conv := currentReplicas
		st.lastConverged = &conv
	}
	if st.lastConverged == nil {
		// Initialise from the observed count when the DHA first adopts a target.
		conv := currentReplicas
		st.lastConverged = &conv
	}

	// Ownership: detect a competing writer that changed replicas out from under us.
	ownershipConflict := st.lastWritten != nil && currentReplicas != *st.lastWritten
	if ownershipConflict {
		marker := appAnnotations[autoscalingv1alpha1.ManagedByAnnotation]
		r.recordEvent(dha, corev1.EventTypeWarning, autoscalingv1alpha1.ReasonOwnershipConflict,
			fmt.Sprintf("replicas changed to %d by a competing writer (marker=%q); not entering a write war", currentReplicas, marker))
	}

	// Reject a non-positive metric target with a distinct reason, so a spec typo
	// (averageValue: "0") is not silently reported as MetricUnavailable.
	for _, m := range dha.Spec.Metrics {
		if metricTargetValue(m) <= 0 {
			apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
				Type:               autoscalingv1alpha1.ConditionAbleToScale,
				Status:             metav1.ConditionFalse,
				Reason:             autoscalingv1alpha1.ReasonInvalidTarget,
				Message:            fmt.Sprintf("metric %s has a non-positive target", m.Type),
				ObservedGeneration: dha.Generation,
			})
			return r.finish(ctx, dha, ctrl.Result{RequeueAfter: requeueInterval})
		}
	}

	// Metrics.
	obs, metricAvailable := r.collectMetrics(ctx, dha, adapter, currentReplicas)
	lagBraked := r.lagBraked(ctx, dha, adapter)

	// Quota ceiling.
	quotaMax := r.quotaCeiling(ctx, dha.Namespace, appValues, currentReplicas)

	up, down := resolveBehavior(dha)
	in := ScaleInput{
		Now:                 r.now(),
		Current:             currentReplicas,
		PrimaryCount:        adapter.PrimaryCount(),
		Min:                 *dha.Spec.MinReplicas,
		Max:                 *dha.Spec.MaxReplicas,
		QuorumFloor:         adapter.QuorumFloor(appValues),
		RespectQuorum:       dha.Spec.Constraints == nil || dha.Spec.Constraints.RespectQuorum,
		QuotaMaxReplicas:    quotaMax,
		Metrics:             obs,
		MetricAvailable:     metricAvailable,
		LagBraked:           lagBraked,
		Operational:         operational,
		ScaleInFlight:       scaleInFlight,
		ScaleUpStep:         up.step,
		ScaleDownStep:       down.step,
		ScaleUpWindow:       up.window,
		ScaleDownWindow:     down.window,
		RecommendHistory:    st.history,
		ConvergenceDeadline: resolveConvergenceDeadline(dha),
		InFlightSince:       st.inFlightSince,
		LastConverged:       st.lastConverged,
		DryRun:              dha.Spec.DryRun,
	}

	decision := Decide(in)

	// Record the raw recommendation for the stabilization window — but only when a
	// metric was actually read. A metric outage yields RawDesired=0 (fail-safe),
	// and recording that 0 would drag the scale-up window's minimum down and
	// suppress scale-up for the whole window after recovery.
	if metricAvailable {
		st.history = appendHistory(st.history, r.now(), decision.RawDesired, max(up.window, down.window))
	}

	r.applyDecisionToStatus(dha, in, decision, obs, ownershipConflict)
	exportMetrics(dha.Namespace, dha.Name, in, decision, ownershipConflict)

	// Clear the backoff once load no longer wants the failed (larger) size.
	if st.failedTarget != nil && decision.Desired < *st.failedTarget {
		st.failedTarget = nil
		st.backoffUntil = nil
		st.backoffCount = 0
	}

	// Backoff: if a scale-up targets the size that just failed to converge and we
	// are still within the backoff window, do not re-attempt — hold at StuckScaling
	// so a persistently unschedulable target does not thrash patch->stuck->rollback.
	if decision.Kind == DecisionScale && st.failedTarget != nil && decision.Desired == *st.failedTarget &&
		st.backoffUntil != nil && r.now().Before(*st.backoffUntil) {
		apimeta.SetStatusCondition(&dha.Status.Conditions, metav1.Condition{
			Type:               autoscalingv1alpha1.ConditionAbleToScale,
			Status:             metav1.ConditionFalse,
			Reason:             autoscalingv1alpha1.ReasonStuckScaling,
			Message:            fmt.Sprintf("backing off re-attempt of %d replicas until %s", decision.Desired, st.backoffUntil.Format(time.RFC3339)),
			ObservedGeneration: dha.Generation,
		})
		return r.finish(ctx, dha, ctrl.Result{RequeueAfter: requeueInterval})
	}

	// Apply the patch when the decision calls for it and we are not in dryRun and
	// there is no competing writer to fight.
	if (decision.Kind == DecisionScale || decision.Kind == DecisionRollback) && !dha.Spec.DryRun && !ownershipConflict {
		if err := r.patchReplicas(ctx, mapping, dha, decision.Desired); err != nil {
			logger.Error(err, "patch replicas")
			r.recordEvent(dha, corev1.EventTypeWarning, "PatchFailed", err.Error())
			return r.finish(ctx, dha, ctrl.Result{RequeueAfter: requeueInterval})
		}
		now := r.now()
		st.inFlightSince = &now
		written := decision.Desired
		st.lastWritten = &written
		// Persist the written count so the ownership back-off survives a restart.
		dha.Status.LastAppliedReplicas = &written
		dha.Status.LastScaleTime = &metav1.Time{Time: now}
		verb := "scaled"
		if decision.Kind == DecisionRollback {
			verb = "rolled back"
			// Record the size that failed to converge and set an exponential backoff
			// before it may be re-attempted.
			failed := currentReplicas
			if st.failedTarget != nil && *st.failedTarget == failed {
				st.backoffCount++
			} else {
				st.backoffCount = 1
			}
			st.failedTarget = &failed
			until := now.Add(backoffDuration(resolveConvergenceDeadline(dha), st.backoffCount))
			st.backoffUntil = &until
		}
		r.recordEvent(dha, corev1.EventTypeNormal, "Scaled",
			fmt.Sprintf("%s %s/%s replicas %d -> %d", verb, dha.Spec.TargetRef.Kind, dha.Spec.TargetRef.Name, currentReplicas, decision.Desired))
	}

	return r.finish(ctx, dha, ctrl.Result{RequeueAfter: requeueInterval})
}

// loadApplication fetches the target Application via the dynamic client and
// returns its values map, the current replicas, its annotations, and the REST
// mapping (for the subsequent patch).
func (r *Reconciler) loadApplication(ctx context.Context, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, adapter TopologyAdapter) (map[string]any, int32, map[string]string, *apimeta.RESTMapping, error) {
	group := dha.Spec.TargetRef.APIGroup
	if group == "" {
		group = autoscalingv1alpha1.DefaultAPIGroup
	}
	mapping, err := r.RESTMapping(schema.GroupKind{Group: group, Kind: dha.Spec.TargetRef.Kind})
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("resolve %s/%s: %w", group, dha.Spec.TargetRef.Kind, err)
	}
	obj, err := r.Resource(mapping.Resource).Namespace(dha.Namespace).Get(ctx, dha.Spec.TargetRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("get application %s: %w", dha.Spec.TargetRef.Name, err)
	}
	spec, _ := obj.Object["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	replicas := nestedInt(spec, 0, adapter.ReplicasPath())
	return spec, replicas, obj.GetAnnotations(), mapping, nil
}

// loadWorkloadMonitor reads the linked WorkloadMonitor. When it is absent the
// loop degrades optimistically (operational, converged) rather than freezing,
// since a managed database always ships one.
func (r *Reconciler) loadWorkloadMonitor(ctx context.Context, namespace, name string, currentReplicas int32) (bool, int32) {
	wm := &cozyv1alpha1.WorkloadMonitor{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, wm); err != nil {
		return true, currentReplicas
	}
	operational := true
	if wm.Status.Operational != nil {
		operational = *wm.Status.Operational
	}
	return operational, wm.Status.AvailableReplicas
}

// metricTargetValue interprets a metric target in the same unit as its driver
// query. ReadCPUUtilization is millicores (the query yields millicores), so the
// target is read as a Kubernetes CPU quantity — "250m" => 250, "1" => 1000 —
// which matches how operators write CPU. Other metrics are plain numbers.
func metricTargetValue(m autoscalingv1alpha1.MetricSpec) float64 {
	if m.Type == autoscalingv1alpha1.MetricReadCPUUtilization {
		return float64(m.Target.AverageValue.MilliValue())
	}
	return m.Target.AverageValue.AsApproximateFloat64()
}

// collectMetrics queries VictoriaMetrics for each driver metric, averaged over
// the read-serving replicas. metricAvailable is false if any query fails.
func (r *Reconciler) collectMetrics(ctx context.Context, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, adapter TopologyAdapter, currentReplicas int32) ([]MetricObservation, bool) {
	baseURL := r.resolveVMSelectURL(ctx, dha.Namespace)
	if baseURL == "" {
		return nil, false
	}
	app := types.NamespacedName{Namespace: dha.Namespace, Name: dha.Spec.TargetRef.Name}
	rcur := currentReplicas - adapter.PrimaryCount()
	if rcur < 1 {
		rcur = 1
	}
	obs := make([]MetricObservation, 0, len(dha.Spec.Metrics))
	for _, m := range dha.Spec.Metrics {
		target := metricTargetValue(m)
		if target <= 0 {
			// A non-positive target is rejected: treat as unavailable rather than
			// dividing by it.
			return nil, false
		}
		q := adapter.DriverQuery(app, m.Type)
		raw, ok, err := r.VM.QueryScalar(ctx, baseURL, q)
		if err != nil || !ok {
			return nil, false
		}
		obs = append(obs, MetricObservation{
			Type:              string(m.Type),
			AveragePerReplica: raw / float64(rcur),
			Target:            target,
		})
	}
	return obs, len(obs) > 0
}

// lagBraked reports whether replication lag exceeds the threshold while the
// primary is actively writing (write-activity gated, §5).
func (r *Reconciler) lagBraked(ctx context.Context, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, adapter TopologyAdapter) bool {
	threshold := defaultMaxReplicationLag
	if dha.Spec.Constraints != nil && dha.Spec.Constraints.MaxReplicationLagSeconds != nil {
		threshold = *dha.Spec.Constraints.MaxReplicationLagSeconds
	}
	baseURL := r.resolveVMSelectURL(ctx, dha.Namespace)
	if baseURL == "" {
		return false
	}
	app := types.NamespacedName{Namespace: dha.Namespace, Name: dha.Spec.TargetRef.Name}
	lagQuery := adapter.ReplicationLagQuery(app)
	if lagQuery == "" {
		// Adapter provides no replication-lag signal (e.g. Redis has no seconds
		// gauge) — do not brake on lag.
		return false
	}
	lag, ok, _ := r.VM.QueryScalar(ctx, baseURL, lagQuery)
	if !ok || lag <= float64(threshold) {
		return false
	}
	// Lag is high; only brake if the primary is actively writing.
	writeRate, ok, _ := r.VM.QueryScalar(ctx, baseURL, adapter.WriteActivityQuery(app))
	if !ok {
		// Cannot confirm write activity: be conservative and brake, since lag is high.
		return true
	}
	return writeRate > 0
}

// quotaCeiling computes the largest total instance count that fits the tenant
// quota, or nil when unbounded/unknown.
func (r *Reconciler) quotaCeiling(ctx context.Context, namespace string, appValues map[string]any, currentReplicas int32) *int32 {
	cpu, mem, ok := PerPodResources(appValues)
	if !ok {
		return nil
	}
	rqs := &corev1.ResourceQuotaList{}
	if err := r.List(ctx, rqs, client.InNamespace(namespace)); err != nil {
		return nil
	}
	if len(rqs.Items) == 0 {
		return nil
	}
	return MaxReplicasWithinQuota(rqs.Items, currentReplicas, cpu, mem)
}

// resolveVMSelectURL finds the tenant's vmselect base URL, using the injected
// resolver when set, otherwise the namespace's monitoring label.
func (r *Reconciler) resolveVMSelectURL(ctx context.Context, namespace string) string {
	if r.BaseURLFor != nil {
		return r.BaseURLFor(ctx, namespace)
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		return ""
	}
	monitoringNS := ns.Labels[namespaceMonitoringLabel]
	if monitoringNS == "" {
		return ""
	}
	return ResolveVMSelectURL(monitoringNS)
}

// patchReplicas writes the new replica count via a JSON merge patch and stamps
// the managed-by marker, claiming ownership. SSA field-level ownership does not
// hold on the aggregated apps API (opaque spec, managedFields not round-tripped),
// so enforcement against competing writers is the admission webhook's job.
func (r *Reconciler) patchReplicas(ctx context.Context, mapping *apimeta.RESTMapping, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, desired int32) error {
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}},"spec":{%q:%d}}`,
		autoscalingv1alpha1.ManagedByAnnotation, dha.Name,
		AdapterFor(dha.Spec.TargetRef.Kind).ReplicasPath(), desired))
	_, err := r.Resource(mapping.Resource).Namespace(dha.Namespace).Patch(
		ctx, dha.Spec.TargetRef.Name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: autoscalingv1alpha1.FieldManager})
	return err
}

func (r *Reconciler) recordEvent(dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, eventType, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(dha, eventType, reason, msg)
	}
}

// finish persists the status and returns the result.
func (r *Reconciler) finish(ctx context.Context, dha *autoscalingv1alpha1.DatabaseHorizontalAutoscaler, res ctrl.Result) (ctrl.Result, error) {
	dha.Status.ObservedGeneration = dha.Generation
	if err := r.Status().Update(ctx, dha); err != nil {
		return ctrl.Result{}, err
	}
	return res, nil
}

// SetupWithManager wires the reconciler into the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1alpha1.DatabaseHorizontalAutoscaler{}).
		Complete(r)
}
