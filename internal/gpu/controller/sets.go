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
	"slices"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// unattachedSetMember selects the claims a VM's set moves, goes Lost and
// is rebound with: those no VMI uses. An attached claim follows its VMI.
func unattachedSetMember(d *gpuv1alpha1.GPUClaim) bool {
	return (holds(d) || d.Status.Phase == gpuv1alpha1.GPUClaimLost) &&
		d.Status.AttachedTo == nil && d.DeletionTimestamp.IsZero()
}

func allLost(set []*gpuv1alpha1.GPUClaim) bool {
	for _, d := range set {
		if d.Status.Phase != gpuv1alpha1.GPUClaimLost {
			return false
		}
	}
	return true
}

// mixesLost reports a set that breaks the set invariant.
func mixesLost(set []*gpuv1alpha1.GPUClaim) bool {
	lost := 0
	for _, d := range set {
		if d.Status.Phase == gpuv1alpha1.GPUClaimLost {
			lost++
		}
	}
	return lost > 0 && lost < len(set)
}

// normalizeSets restores the set invariant, which holds after every pass
// in which no write failed:
//
//	The unattached claims one VM names are either all Lost or all held.
//	A set that had a Lost member is held on one node.
//
// Held sets may otherwise sit on several nodes, as separate orders bind
// apart; the cordon-only stay and a Stay pin keep a held set in place.
// Every placement step changes a set as a whole. A write that failed half
// way through a set breaks the invariant, and so does a VMI that ended
// and left its Lost claim unattached next to held members. This step runs
// right after attachments, before any placement, and gathers such a set
// onto one node with room for all of it or sends all of it to Lost.
func (r *Reconciler) normalizeSets(ctx context.Context, w *world) []error {
	var errs []error
	for _, set := range w.vmSets(unattachedSetMember) {
		if w.aborted {
			break
		}
		if !mixesLost(set) {
			continue
		}
		if target := w.pick(set); target != "" {
			before := map[*gpuv1alpha1.GPUClaim]string{}
			wasLost := map[*gpuv1alpha1.GPUClaim]bool{}
			for _, d := range set {
				before[d] = string(d.Status.Phase) + "@" + d.Status.NodeName
				wasLost[d] = d.Status.Phase == gpuv1alpha1.GPUClaimLost
			}
			w.placeOn(set, target)
			// Held members first: until the last Lost member is written the
			// set still mixes Lost and held, so a write that fails part way
			// leaves it for the next pass to gather, never held but split.
			slices.SortStableFunc(set, func(a, b *gpuv1alpha1.GPUClaim) int { return boolInt(wasLost[a]) - boolInt(wasLost[b]) })
			errs = append(errs, r.writeSet(ctx, w, set, func(d *gpuv1alpha1.GPUClaim) bool {
				return before[d] != string(d.Status.Phase)+"@"+d.Status.NodeName
			}))
			continue
		}
		var changed []*gpuv1alpha1.GPUClaim
		for _, d := range set {
			if d.Status.Phase == gpuv1alpha1.GPUClaimLost {
				continue
			}
			d.Status.Phase = gpuv1alpha1.GPUClaimLost
			w.setWaiting(d, ReasonCapacity, "another claim of its set is Lost and no node has room for the whole set")
			w.setClaim(d)
			changed = append(changed, d)
		}
		errs = append(errs, r.writeSet(ctx, w, changed, func(*gpuv1alpha1.GPUClaim) bool { return true }))
	}
	return []error{errors.Join(errs...)}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
