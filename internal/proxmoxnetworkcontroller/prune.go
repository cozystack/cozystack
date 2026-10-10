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

package proxmoxnetworkcontroller

import (
	"context"
	"encoding/json"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

// Kube-OVN lists a subnet in Vlan.status.subnets from the first time it binds
// the subnet to the Vlan until its delete handler has deleted the subnet's
// logical switch, localnet port included. Nothing else ever prunes the list.
// Both the network finalizer and the zone's orphan collection read it the same
// way: a listed subnet that still exists holds the VLAN with no time limit, and
// a listed subnet that is gone holds it until Kube-OVN prunes the name, or for
// at most the prune timeout from when the controller first saw it gone.
//
// The timers outlive the wait that started them only where that is wanted:
// from a deleting network to the orphan collection that takes over when its
// finalizer is removed by hand. A network recreated under the same name drops
// them when it adopts the Vlan (adoptVlan), and a deleting network restarts
// any that started before its own deletion; either way a subnet that the new
// network deletes under an old name gets a full wait of its own.

// clock is a time source; the zero value is the wall clock.
type clock func() time.Time

// Now returns the current time.
func (c clock) Now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// splitSubnets sorts the names into those that still exist as Kube-OVN
// Subnets, Terminating ones included, and those that do not.
func splitSubnets(ctx context.Context, reader client.Reader, names []string) (live, gone []string, err error) {
	for _, n := range names {
		switch err = reader.Get(ctx, client.ObjectKey{Name: n}, &Subnet{}); {
		case err == nil:
			live = append(live, n)
		case apierrors.IsNotFound(err):
			gone = append(gone, n)
		default:
			return nil, nil, err
		}
	}
	return live, gone, nil
}

// trackDeletedSubnets keeps one prune timer per deleted subnet on the Vlan, in
// pxv1.AnnotationDeletedSubnets, and returns when the newest of them started.
// The VLAN may be released once that newest timer has run out: a single timer
// for the whole Vlan would hand a subnet deleted later whatever was left of an
// earlier one's wait. A name already tracked keeps its start, a new one starts
// now, and a name that is no longer gone is dropped, so it starts afresh if it
// is ever gone again. An entry that cannot be read, lies in the future, or
// started before notBefore starts afresh too, which only makes the wait
// longer; a zero notBefore keeps every entry. The timers live on the Vlan
// rather than on the network, so they survive controller restarts and carry
// over to the zone's orphan collection when the network's finalizer is removed
// by hand. It writes the Vlan only when the timers change.
func trackDeletedSubnets(ctx context.Context, c client.Client, v *Vlan, gone []string, now, notBefore time.Time) (time.Time, error) {
	prev := map[string]string{}
	if s, ok := v.Annotations[pxv1.AnnotationDeletedSubnets]; ok {
		_ = json.Unmarshal([]byte(s), &prev)
	}
	now = now.UTC().Truncate(time.Second)
	// Timers are kept to the second, as Kubernetes keeps its timestamps.
	notBefore = notBefore.UTC().Truncate(time.Second)
	tracked := make(map[string]string, len(gone))
	var newest time.Time
	for _, n := range gone {
		since, err := time.Parse(time.RFC3339, prev[n])
		if err != nil || since.After(now) || since.Before(notBefore) {
			since = now
		}
		tracked[n] = since.Format(time.RFC3339)
		if since.After(newest) {
			newest = since
		}
	}

	want := ""
	if len(tracked) > 0 {
		b, err := json.Marshal(tracked) // map keys are sorted, so the value is stable
		if err != nil {
			return time.Time{}, err
		}
		want = string(b)
	}
	if have, ok := v.Annotations[pxv1.AnnotationDeletedSubnets]; have == want && ok == (want != "") {
		return newest, nil
	}
	patch := client.MergeFromWithOptions(v.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if want == "" {
		delete(v.Annotations, pxv1.AnnotationDeletedSubnets)
	} else {
		if v.Annotations == nil {
			v.Annotations = map[string]string{}
		}
		v.Annotations[pxv1.AnnotationDeletedSubnets] = want
	}
	if err := c.Patch(ctx, v, patch); err != nil {
		return time.Time{}, err
	}
	return newest, nil
}

// adoptVlan puts the network finalizer on the Vlan of a network that is not
// being deleted, unless the Vlan is, and drops any prune timers left on it. Such
// a network waits for no prune, so the timers can only be an earlier network's
// of the same name, or the orphan collection's from before this network
// existed. Kept, they would cut short the wait of a subnet this network
// deletes later under a name they track. It writes the Vlan only when there
// is something to change, in a single patch.
func adoptVlan(ctx context.Context, c client.Client, v *Vlan) error {
	_, timers := v.Annotations[pxv1.AnnotationDeletedSubnets]
	finalizer := v.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(v, pxv1.Finalizer)
	if !timers && !finalizer {
		return nil
	}
	patch := client.MergeFromWithOptions(v.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if finalizer {
		controllerutil.AddFinalizer(v, pxv1.Finalizer)
	}
	delete(v.Annotations, pxv1.AnnotationDeletedSubnets)
	return c.Patch(ctx, v, patch)
}
