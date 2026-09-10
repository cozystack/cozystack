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

package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// MemoryStateAnnotation is operator-owned state on the cluster-scoped Namespace,
	// separate from the LimitRange so withdrawing the default does not erase the hold.
	MemoryStateAnnotation = "operator.cozystack.io/system-memory-state"
	// MemoryAcknowledgementAnnotation is set by a cluster administrator to the hold's
	// acknowledgement value after checking the sources of future pods.
	MemoryAcknowledgementAnnotation = "operator.cozystack.io/system-memory-ack"
	memoryStateFieldOwner           = packageControllerFieldOwner + "-memory-state"
)

// memoryDefaultState remembers policy decisions, not an inventory of live pods.
// A missing pod cannot distinguish removal of its controller from a recreation gap.
type memoryDefaultState struct {
	Version   int    `json:"version"`
	LastLimit string `json:"lastLimit,omitempty"`
	Hold      string `json:"hold,omitempty"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func (s memoryDefaultState) acknowledgement() string {
	return s.Hold + ":" + s.Target
}

func (r *PackageReconciler) memoryPolicyReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// readMemoryDefaultState fails closed on corrupt or newer state. In particular, it
// never interprets an unreadable hold as permission to write a default.
func (r *PackageReconciler) readMemoryDefaultState(ctx context.Context, nsName string) (*corev1.Namespace, memoryDefaultState, error) {
	ns := &corev1.Namespace{}
	if err := r.memoryPolicyReader().Get(ctx, types.NamespacedName{Name: nsName}, ns); err != nil {
		return nil, memoryDefaultState{}, err
	}
	state := memoryDefaultState{Version: 1}
	if raw, ok := ns.Annotations[MemoryStateAnnotation]; ok {
		state = memoryDefaultState{}
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return ns, state, fmt.Errorf("invalid system memory state in namespace %s: %w", nsName, err)
		}
		if state.Version != 1 || (state.Hold == "") != (state.Target == "") {
			return ns, state, fmt.Errorf("unsupported or incomplete system memory state in namespace %s", nsName)
		}
		for _, value := range []string{state.LastLimit, state.Target} {
			if value == "" {
				continue
			}
			q, err := resource.ParseQuantity(value)
			if err != nil || q.Sign() <= 0 {
				return ns, state, fmt.Errorf("invalid system memory quantity %q in namespace %s", value, nsName)
			}
		}
	}
	return ns, state, nil
}

// writeMemoryDefaultState uses a separate field manager and optimistic locking:
// the normal Namespace apply must neither prune the state nor race an admin ack.
func (r *PackageReconciler) writeMemoryDefaultState(ctx context.Context, ns *corev1.Namespace, state memoryDefaultState, consumeAck bool) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if ns.Annotations[MemoryStateAnnotation] == string(raw) && (!consumeAck || ns.Annotations[MemoryAcknowledgementAnnotation] == "") {
		return nil
	}
	before := ns.DeepCopy()
	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}
	ns.Annotations[MemoryStateAnnotation] = string(raw)
	if consumeAck {
		delete(ns.Annotations, MemoryAcknowledgementAnnotation)
	}
	return r.Patch(ctx, ns, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(memoryStateFieldOwner))
}

// rememberMemoryDefault preserves the old default before deletion, including the
// first disable of an object written by a version without Namespace state.
func (r *PackageReconciler) rememberMemoryDefault(ctx context.Context, lr *corev1.LimitRange) error {
	ns, state, err := r.readMemoryDefaultState(ctx, lr.Namespace)
	if err != nil {
		// Corrupt state still prevents any later default. Disabling must remain
		// possible without first repairing that state.
		if ns != nil {
			return nil
		}
		return err
	}
	limit := memoryDefaultLimit(lr)
	if limit.IsZero() || (state.LastLimit != "" && limit.Cmp(resource.MustParse(state.LastLimit)) <= 0) {
		return nil
	}
	state.LastLimit = limit.String()
	return r.writeMemoryDefaultState(ctx, ns, state, false)
}

func memoryDefaultLimit(lr *corev1.LimitRange) resource.Quantity {
	for _, item := range lr.Spec.Limits {
		if item.Type == corev1.LimitTypeContainer {
			return item.Default[corev1.ResourceMemory]
		}
	}
	return resource.Quantity{}
}

// authorizeMemoryDefault records the decision before any LimitRange write. An ack
// permits only this hold and this target; it never overrides a visible blocker.
// All observed blockers are held durably, including templates: deletion of a
// template alone is no stronger proof of user intent than deletion of a Pod.
func (r *PackageReconciler) authorizeMemoryDefault(ctx context.Context, nsName string, existing *corev1.LimitRange, blocker *memoryRequestBlocker) (bool, error) {
	ns, state, err := r.readMemoryDefaultState(ctx, nsName)
	if err != nil {
		return false, err
	}
	if old := memoryDefaultLimit(existing); old.Sign() > 0 {
		if state.LastLimit == "" || old.Cmp(resource.MustParse(state.LastLimit)) > 0 {
			state.LastLimit = old.String()
		}
	}
	target := r.SystemNamespaceMemoryLimit.String()
	reason := ""
	if state.LastLimit != "" {
		last := resource.MustParse(state.LastLimit) // validated when reading state
		if r.SystemNamespaceMemoryLimit.Cmp(last) < 0 {
			reason = "lowering a previously applied default requires checking future pod specifications"
		}
	}
	if blocker != nil {
		reason = fmt.Sprintf("%s container %s requests %s without a memory limit", blocker.workload, blocker.container, blocker.request.String())
	}
	sameTarget := false
	if state.Target != "" {
		previousTarget := resource.MustParse(state.Target)
		sameTarget = previousTarget.Cmp(r.SystemNamespaceMemoryLimit) == 0
	}
	if (state.Hold != "" && !sameTarget) || (state.Hold == "" && reason != "") {
		state.Hold = string(uuid.NewUUID())
		state.Target = target
		state.Reason = reason
		if state.Reason == "" {
			state.Reason = "a previous hold remains unresolved"
		}
	}
	if state.Hold != "" {
		if blocker == nil && ns.Annotations[MemoryAcknowledgementAnnotation] == state.acknowledgement() {
			state.Hold, state.Target, state.Reason = "", "", ""
		} else {
			if err := r.writeMemoryDefaultState(ctx, ns, state, blocker != nil); err != nil {
				return false, err
			}
			log.FromContext(ctx).Info("withholding the default container memory limit until explicitly acknowledged",
				"namespace", nsName, "configuredLimit", target, "reason", state.Reason,
				"acknowledgementAnnotation", MemoryAcknowledgementAnnotation, "acknowledgement", state.acknowledgement(),
				"currentBlocker", blocker != nil)
			return false, nil
		}
	}
	// Record before apply. A crash between these writes is conservative: it can
	// demand an extra acknowledgement, but cannot forget a ceiling already used.
	state.LastLimit = target
	if err := r.writeMemoryDefaultState(ctx, ns, state, true); err != nil {
		return false, err
	}
	return true, nil
}

// cleanupUnusedMemoryDefaults removes only this feature's objects from namespaces
// no active Package targets. Decisions consider all Packages sharing a namespace;
// Namespace state survives removal, including disable/re-enable and retargeting.
func (r *PackageReconciler) cleanupUnusedMemoryDefaults(ctx context.Context) error {
	if r.SystemNamespaceMemoryLimit.IsZero() {
		return r.deleteManagedSystemDefaultsLimitRanges(ctx)
	}
	managed := &corev1.LimitRangeList{}
	if err := r.List(ctx, managed, client.MatchingLabels{managedByLabel: packageControllerFieldOwner}); err != nil {
		return err
	}
	if len(managed.Items) == 0 {
		return nil
	}
	packages := &cozyv1alpha1.PackageList{}
	if err := r.List(ctx, packages); err != nil {
		return err
	}
	sources := &cozyv1alpha1.PackageSourceList{}
	if err := r.List(ctx, sources); err != nil {
		return err
	}
	byName := make(map[string]*cozyv1alpha1.PackageSource, len(sources.Items))
	for i := range sources.Items {
		byName[sources.Items[i].Name] = &sources.Items[i]
	}
	active := make(map[string]bool)
	for _, pkg := range packages.Items {
		if !pkg.DeletionTimestamp.IsZero() {
			continue
		}
		source := byName[pkg.Name]
		if source == nil {
			continue
		}
		variantName := pkg.Spec.Variant
		if variantName == "" {
			variantName = "default"
		}
		for _, variant := range source.Spec.Variants {
			if variant.Name != variantName {
				continue
			}
			for _, component := range variant.Components {
				if component.Install == nil || strings.HasPrefix(component.Install.Namespace, "tenant-") {
					continue
				}
				if setting, ok := pkg.Spec.Components[component.Name]; ok && setting.Enabled != nil && !*setting.Enabled {
					continue
				}
				active[component.Install.Namespace] = true
			}
		}
	}
	var firstErr error
	for _, lr := range managed.Items {
		if lr.Name != SystemDefaultsLimitRangeName || lr.Labels[managedByLabel] != packageControllerFieldOwner || active[lr.Namespace] {
			continue
		}
		if err := r.deleteSystemDefaultsLimitRange(ctx, lr.Namespace); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
