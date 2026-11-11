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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

const (
	lrpA   = "vpc-x-subnet-a"
	groupA = "px-vpc-x-subnet-a"
)

func trunkNode(name, chassis string, ready bool) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}, Annotations: map[string]string{}}}
	if ready {
		n.Labels[providerReadyLabel(provider)] = "true"
	}
	if chassis != "" {
		n.Annotations[AnnotationChassis] = chassis
	}
	return n
}

// gatewayFixture is a Ready network on vpc-x with two trunk nodes that have
// a chassis, a trunk node without one, a node off the trunk, and an OVN NB
// that has the subnet's router port.
func gatewayFixture(t *testing.T, extra ...client.Object) (client.Client, *fakeNB, *NetworkReconciler) {
	t.Helper()
	objs := []client.Object{
		namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"),
		pool(tenantA, "subnet-a", "10.208.64", 0),
		trunkNode("worker1", "ch-w1", true), trunkNode("worker2", "ch-w2", true),
		trunkNode("worker3", "", true), trunkNode("node1", "ch-n1", false),
	}
	c := newClient(t, append(objs, extra...)...)
	nb := newFakeNB()
	nb.addRouterPort(lrpA)
	forgetNetwork(tenantA, "subnet-a")
	return c, nb, &NetworkReconciler{Client: c, APIReader: c, Gateways: &GatewayManager{nb: nb}}
}

// wantedChassis is what the group of the network should hold for the nodes
// in the cluster now.
func wantedChassis(t *testing.T, c client.Client, ns, name string) map[string]int {
	t.Helper()
	var nodes corev1.NodeList
	if err := c.List(context.Background(), &nodes, client.MatchingLabels{providerReadyLabel(provider): "true"}); err != nil {
		t.Fatal(err)
	}
	var sub Subnet
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &sub); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, ch := range gatewayChassisFor(nodes.Items, gatewayKeyForVPC(sub.Spec.Vpc)) {
		out[ch.Name] = ch.Priority
	}
	return out
}

func gaugeValue(t *testing.T, g interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

// hasSeries reports whether the vector has a series for the network,
// without creating one.
func hasSeries(t *testing.T, vec *prometheus.GaugeVec, namespace, name string) bool {
	t.Helper()
	ch := make(chan prometheus.Metric, 100)
	vec.Collect(ch)
	close(ch)
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{}
		for _, l := range d.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		if labels["namespace"] == namespace && labels["network"] == name {
			return true
		}
	}
	return false
}

func TestGatewayGroupIsCreatedAndTheRouterPortPointsAtIt(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	res := reconcileNetWith(t, r, tenantA, "subnet-a")

	g := nb.group(groupA)
	if g == nil {
		t.Fatal("no HA chassis group was created")
	}
	if want := map[string]string{"owner": "proxmox-network", "network": "tenant-a/subnet-a"}; !maps.Equal(g.ExternalIDs, want) {
		t.Fatalf("external_ids = %v, want %v", g.ExternalIDs, want)
	}
	want := wantedChassis(t, c, tenantA, "subnet-a")
	if !maps.Equal(g.Chassis, want) || len(want) != 2 || want["ch-w1"]+want["ch-w2"] != 190 {
		t.Fatalf("chassis = %v, want the two trunk nodes with a chassis at 100 and 90 (%v)", g.Chassis, want)
	}
	if got := nb.portGroup(t, lrpA); got != groupA {
		t.Fatalf("router port points at %q, want %q", got, groupA)
	}
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionTrue, pxv1.GatewayReasonChassisAssigned)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if res.RequeueAfter != gatewayResyncPeriod {
		t.Fatalf("requeue after %v, want the gateway resync %v", res.RequeueAfter, gatewayResyncPeriod)
	}
	if v := gaugeValue(t, networkGatewayChassis.WithLabelValues(tenantA, "subnet-a")); v != 2 {
		t.Fatalf("gateway chassis metric = %v, want 2", v)
	}
	if v := gaugeValue(t, networkGatewayReady.WithLabelValues(tenantA, "subnet-a")); v != 1 {
		t.Fatalf("gateway ready metric = %v, want 1", v)
	}

	// A second pass finds nothing to change and writes nothing.
	writes := nb.writeCount()
	reconcileNetWith(t, r, tenantA, "subnet-a")
	if nb.writeCount() != writes {
		t.Fatal("an up-to-date group must not be written again")
	}
}

// All Proxmox router ports of one VPC must share their active chassis: a
// packet from a VM to the egress gateway on the VPC's internal subnet then
// leaves the router where it entered, through a localnet port, instead of
// crossing the Geneve tunnel, where Cilium rewrites LoadBalancer IPs.
func TestGatewayNetworksOfOneVPCShareTheirActiveChassis(t *testing.T) {
	c, nb, r := gatewayFixture(t,
		network(tenantA, "subnet-b"), subnet("subnet-b", "10.208.65"), pool(tenantA, "subnet-b", "10.208.65", 0),
		trunkNode("worker4", "ch-w4", true), trunkNode("worker5", "ch-w5", true))
	nb.addRouterPort("vpc-x-subnet-b")
	forgetNetwork(tenantA, "subnet-b")
	reconcileNetWith(t, r, tenantA, "subnet-a")
	reconcileNetWith(t, r, tenantA, "subnet-b")

	a, b := nb.group(groupA), nb.group("px-vpc-x-subnet-b")
	if a == nil || b == nil {
		t.Fatal("both networks must get a group")
	}
	if !maps.Equal(a.Chassis, b.Chassis) {
		t.Fatalf("networks of one VPC got different chassis orders: %v and %v", a.Chassis, b.Chassis)
	}
	// And the order is the VPC's, whatever the network is called.
	if want := wantedChassis(t, c, tenantA, "subnet-b"); !maps.Equal(b.Chassis, want) {
		t.Fatalf("chassis = %v, want the VPC's order %v", b.Chassis, want)
	}
}

func TestGatewayAdoptsTheUnownedGroupOfTheSameName(t *testing.T) {
	// A group made by hand before the controller managed it: no owner in
	// external_ids, priorities 20/10, and here a chassis that is no longer a
	// trunk node.
	c, nb, r := gatewayFixture(t)
	uuid := nb.addGroup(groupA, map[string]string{"note": "manual"}, map[string]int{"ch-w1": 20, "ch-w2": 10, "ch-gone": 5}, lrpA)

	reconcileNetWith(t, r, tenantA, "subnet-a")

	g := nb.group(groupA)
	if g == nil || g.UUID != uuid {
		t.Fatalf("the existing group must be adopted in place, got %+v", g)
	}
	if want := map[string]string{"owner": "proxmox-network", "network": "tenant-a/subnet-a", "note": "manual"}; !maps.Equal(g.ExternalIDs, want) {
		t.Fatalf("external_ids = %v, want %v", g.ExternalIDs, want)
	}
	if want := wantedChassis(t, c, tenantA, "subnet-a"); !maps.Equal(g.Chassis, want) {
		t.Fatalf("chassis = %v, want %v", g.Chassis, want)
	}
	if n := nb.count(tableHAChassis); n != 2 {
		t.Fatalf("%d HA_Chassis rows left, want 2: the stale one goes with its reference", n)
	}
	if nb.count(tableHAChassisGrp) != 1 {
		t.Fatal("adoption must not create a second group")
	}
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionTrue, pxv1.GatewayReasonChassisAssigned)
}

func TestGatewayLeavesAForeignGroupAlone(t *testing.T) {
	for name, ids := range map[string]map[string]string{
		"other owner":               {"owner": "kube-ovn"},
		"owned for another network": {"owner": "proxmox-network", "network": "tenant-b/subnet-a"},
		"other owner, same network": {"owner": "kube-ovn", "network": "tenant-a/subnet-a"},
	} {
		t.Run(name, func(t *testing.T) {
			c, nb, r := gatewayFixture(t)
			nb.addGroup(groupA, ids, map[string]int{"ch-n1": 50})
			reconcileNetWith(t, r, tenantA, "subnet-a")

			g := nb.group(groupA)
			if !maps.Equal(g.ExternalIDs, ids) || !maps.Equal(g.Chassis, map[string]int{"ch-n1": 50}) {
				t.Fatalf("a foreign group was changed: %+v", g)
			}
			if got := nb.portGroup(t, lrpA); got != "" {
				t.Fatalf("the router port was pointed at a foreign group %q", got)
			}
			if nb.writeCount() != 0 {
				t.Fatal("nothing may be written next to a foreign group")
			}
			n := getNet(t, c, tenantA, "subnet-a")
			wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonForeignGroup)
			wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonForeignGroup)
		})
	}
}

func TestGatewayChassisRotation(t *testing.T) {
	names := []string{"n-c", "n-a", "n-d", "n-b"}
	nodes := func(order []string) []corev1.Node {
		var out []corev1.Node
		for _, n := range order {
			out = append(out, *trunkNode(n, "ch-"+n, true))
		}
		return out
	}
	active := func(order []string, net string) string {
		return gatewayChassisFor(nodes(order), "tenant/"+net)[0].Name
	}

	// Deterministic, whatever order the nodes are listed in, with
	// priorities 100, 90, 80, 70 over all four nodes.
	a := gatewayChassisFor(nodes(names), "t/net")
	b := gatewayChassisFor(nodes([]string{"n-b", "n-d", "n-a", "n-c"}), "t/net")
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("order depends on the node list: %v vs %v", a, b)
	}
	seenChassis := map[string]bool{}
	for i, ch := range a {
		seenChassis[ch.Name] = true
		if ch.Priority != 100-10*i {
			t.Fatalf("chassis %d = %+v, want priority %d (%v)", i, ch, 100-10*i, a)
		}
	}
	if len(seenChassis) != 4 {
		t.Fatalf("want every node once: %v", a)
	}

	// Spread: the active gateways of many networks land evenly on the nodes.
	const nets = 400
	top := map[string]int{}
	for i := range nets {
		top[active(names, fmt.Sprintf("net-%d", i))]++
	}
	if len(top) != 4 {
		t.Fatalf("active gateways of %d networks sit on %d of 4 nodes: %v", nets, len(top), top)
	}
	for n, k := range top {
		if k < nets/4*6/10 {
			t.Fatalf("node %s is active for only %d of %d networks: %v", n, k, nets, top)
		}
	}

	// A new node takes over only the networks it wins, about 1/5 of them,
	// and leaves the order of the other nodes alone.
	grown := append(slices.Clone(names), "n-e")
	moved := 0
	for i := range nets {
		net := fmt.Sprintf("net-%d", i)
		before, after := active(names, net), active(grown, net)
		if before != after {
			moved++
			if after != "ch-n-e" {
				t.Fatalf("%s moved from %s to %s, not to the new node", net, before, after)
			}
		}
		var rest []string
		for _, ch := range gatewayChassisFor(nodes(grown), "tenant/"+net) {
			if ch.Name != "ch-n-e" {
				rest = append(rest, ch.Name)
			}
		}
		var old []string
		for _, ch := range gatewayChassisFor(nodes(names), "tenant/"+net) {
			old = append(old, ch.Name)
		}
		if !slices.Equal(rest, old) {
			t.Fatalf("%s: the other nodes were reordered: %v -> %v", net, old, rest)
		}
	}
	if moved < nets/5/2 || moved > nets/5*2 {
		t.Fatalf("a fifth node moved %d of %d networks, want about %d", moved, nets, nets/5)
	}

	// A node that leaves moves only the networks it was active for.
	shrunk := []string{"n-b", "n-c", "n-d"}
	for i := range nets {
		net := fmt.Sprintf("net-%d", i)
		before, after := active(names, net), active(shrunk, net)
		if before != after && before != "ch-n-a" {
			t.Fatalf("%s moved from %s to %s although n-a left", net, before, after)
		}
	}

	// More than ten nodes: still distinct and positive.
	var many []string
	for i := range 12 {
		many = append(many, fmt.Sprintf("n-%02d", i))
	}
	seen := map[int]bool{}
	for _, ch := range gatewayChassisFor(nodes(many), "t/net") {
		if ch.Priority <= 0 || seen[ch.Priority] {
			t.Fatalf("priority %d repeats or is not positive", ch.Priority)
		}
		seen[ch.Priority] = true
	}

	// Nodes without a chassis, being deleted, or sharing a chassis are skipped.
	gone := trunkNode("n-e", "ch-n-e", true)
	gone.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	gone.Finalizers = []string{"x"}
	dup := trunkNode("n-z", "ch-n-a", true) // n-a, listed after it, keeps the chassis
	for _, extra := range [][]corev1.Node{{*trunkNode("n-g", "", true)}, {*gone}, {*dup}} {
		for _, list := range [][]corev1.Node{append(nodes(names), extra...), append(slices.Clone(extra), nodes(names)...)} {
			if got := gatewayChassisFor(list, "t/net"); fmt.Sprint(got) != fmt.Sprint(a) {
				t.Fatalf("got %v, want %v", got, a)
			}
		}
	}
	if gatewayChassisFor(nil, "t/net") != nil {
		t.Fatal("no nodes, no chassis")
	}
}

func TestGatewayFollowsTheTrunkNodes(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	uuid := nb.group(groupA).UUID

	// worker2 leaves the provider network, worker3 gets its chassis, and
	// worker1's chassis is renamed.
	for name, mutate := range map[string]func(*corev1.Node){
		"worker2": func(n *corev1.Node) { delete(n.Labels, providerReadyLabel(provider)) },
		"worker3": func(n *corev1.Node) { n.Annotations[AnnotationChassis] = "ch-w3" },
		"worker1": func(n *corev1.Node) { n.Annotations[AnnotationChassis] = "ch-w1-new" },
	} {
		n := &corev1.Node{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: name}, n); err != nil {
			t.Fatal(err)
		}
		if n.Annotations == nil {
			n.Annotations = map[string]string{}
		}
		mutate(n)
		if err := c.Update(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	reconcileNetWith(t, r, tenantA, "subnet-a")

	g := nb.group(groupA)
	want := wantedChassis(t, c, tenantA, "subnet-a")
	if g.UUID != uuid || !maps.Equal(g.Chassis, want) {
		t.Fatalf("group %s chassis = %v, want %s with %v", g.UUID, g.Chassis, uuid, want)
	}
	if _, ok := want["ch-w3"]; !ok || len(want) != 2 {
		t.Fatalf("want = %v", want)
	}
	if n := nb.count(tableHAChassis); n != 2 {
		t.Fatalf("%d HA_Chassis rows, want 2", n)
	}
}

func TestGatewayRepairsDrift(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")

	// Someone clears the router port.
	nb.clearPortGroup(lrpA)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	if got := nb.portGroup(t, lrpA); got != groupA {
		t.Fatalf("router port points at %q after repair", got)
	}

	// Kube-OVN recreates the router port without a group.
	nb.deleteRouterPort(lrpA)
	nb.addRouterPort(lrpA)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	if got := nb.portGroup(t, lrpA); got != groupA {
		t.Fatalf("recreated router port points at %q", got)
	}
	if nb.count(tableHAChassisGrp) != 1 {
		t.Fatal("repair must reuse the group")
	}
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
}

func TestGatewayWaitsForTheRouterPort(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	nb.deleteRouterPort(lrpA)
	reconcileNetWith(t, r, tenantA, "subnet-a")

	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonRouterPortMissing)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonRouterPortMissing)
	if nb.count(tableHAChassisGrp) != 0 {
		t.Fatal("no group may be created for a router port that does not exist")
	}
	if v := gaugeValue(t, networkGatewayChassis.WithLabelValues(tenantA, "subnet-a")); v != 0 {
		t.Fatalf("gateway chassis metric = %v, want 0", v)
	}
	if v := gaugeValue(t, networkGatewayReady.WithLabelValues(tenantA, "subnet-a")); v != 0 {
		t.Fatalf("gateway ready metric = %v, want 0", v)
	}

	nb.addRouterPort(lrpA)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
}

func TestGatewayWithoutTrunkNodes(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	for _, name := range []string{"worker1", "worker2"} {
		n := &corev1.Node{}
		_ = c.Get(context.Background(), client.ObjectKey{Name: name}, n)
		if err := c.Delete(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNoTrunkNodes)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonNoTrunkNodes)
	if nb.count(tableHAChassisGrp) != 0 {
		t.Fatal("an empty group would be no gateway either; none may be written")
	}
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func TestGatewayNBUnavailable(t *testing.T) {
	// A network whose gateway was never up turns False straight away.
	c, nb, r := gatewayFixture(t)
	nb.setErr(errors.New("connect to OVN NB: connection refused"))
	errs := counterValue(t, gatewayNBErrors)
	res := reconcileNetWith(t, r, tenantA, "subnet-a")

	if res.RequeueAfter != gatewayRetryPeriod {
		t.Fatalf("requeue after %v, want %v", res.RequeueAfter, gatewayRetryPeriod)
	}
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	// The rest of the network is still evaluated.
	wantCond(t, n, pxv1.ConditionVLANAllocated, metav1.ConditionTrue, "Allocated")
	if counterValue(t, gatewayNBErrors) != errs+1 {
		t.Fatal("a failed pass must be counted")
	}

	nb.setErr(nil)
	res = reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if res.RequeueAfter != gatewayResyncPeriod {
		t.Fatalf("requeue after %v, want %v", res.RequeueAfter, gatewayResyncPeriod)
	}
}

// An NB outage does not take Ready from a network whose gateway was up: the
// group is still there and so is the VMs' gateway. Only an outage longer
// than the grace period does.
func TestGatewayNBOutageKeepsReadyForTheGracePeriod(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	before := cond(getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady).DeepCopy()

	nb.setErr(errors.New("connect to OVN NB: i/o timeout"))
	for _, after := range []time.Duration{0, 5 * time.Minute, gatewayNBGracePeriod - time.Minute} {
		r.now = later(after)
		res := reconcileNetWith(t, r, tenantA, "subnet-a")
		n := getNet(t, c, tenantA, "subnet-a")
		wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionTrue, pxv1.GatewayReasonChassisAssigned)
		wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
		if got := cond(n, pxv1.ConditionGatewayReady); got.Message != before.Message || !got.LastTransitionTime.Equal(&before.LastTransitionTime) {
			t.Fatalf("GatewayReady changed during the grace: %+v", got)
		}
		if res.RequeueAfter != gatewayRetryPeriod {
			t.Fatalf("requeue after %v, want %v while the NB fails", res.RequeueAfter, gatewayRetryPeriod)
		}
	}
	r.now = later(gatewayNBGracePeriod + time.Second)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)

	// Back: True again, and a later outage gets a fresh grace.
	nb.setErr(nil)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	nb.setErr(errors.New("connection refused"))
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
}

// Switching management on for a network that is Ready, as on the upgrade
// that brings this feature, while the NB cannot be reached: Ready stays for
// the grace period, GatewayReady says it does not know, and a restart in
// between does not start the grace again.
func TestGatewayNBOutageWhenManagementIsSwitchedOn(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	r.Gateways = nil
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")

	nb.setErr(errors.New("x509: certificate signed by unknown authority"))
	r.Gateways = &GatewayManager{nb: nb}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionUnknown, pxv1.GatewayReasonNBUnavailable)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if v := gaugeValue(t, networkGatewayReady.WithLabelValues(tenantA, "subnet-a")); v != 0 {
		t.Fatalf("gateway ready metric = %v, want 0 while unknown", v)
	}

	// A restart: the in-memory record is gone, the condition's transition
	// time still dates the outage.
	r.Gateways = &GatewayManager{nb: nb}
	r.now = later(gatewayNBGracePeriod + time.Second)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n = getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
}

func TestGatewayWaitsForTheVLAN(t *testing.T) {
	// A network that cannot get a VLAN never reaches the NB, and a stale
	// GatewayReady=True does not linger.
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	z := &pxv1.ProxmoxNetworkZone{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: zoneName}, z)
	if err := c.Delete(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	nb.setErr(errors.New("must not be called"))
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionUnknown, "WaitingForVLAN")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "ZoneNotFound")
}

func TestGatewayIsReleasedBeforeTheVLAN(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	// Another network's group and an unowned one stay.
	nb.addRouterPort("vpc-y-subnet-z")
	nb.addGroup("px-vpc-y-subnet-z", map[string]string{"owner": "proxmox-network", "network": "tenant-b/subnet-z"}, map[string]int{"ch-w1": 100}, "vpc-y-subnet-z")
	nb.addGroup("px-by-hand", nil, map[string]int{"ch-w2": 1})

	deleteNet(t, c, tenantA, "subnet-a")
	s := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, s)
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// First pass lets the subnet go. The router port is still in OVN, as
	// when Kube-OVN has not got to it yet.
	reconcileNetWith(t, r, tenantA, "subnet-a")

	// The NB is down: the VLAN is held.
	nb.setErr(errors.New("connection refused"))
	res := reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "GatewayReleasePending")
	if res.RequeueAfter != gatewayRetryPeriod {
		t.Fatalf("requeue after %v, want %v", res.RequeueAfter, gatewayRetryPeriod)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); err != nil {
		t.Fatalf("the VLAN must not be released before the group is gone: %v", err)
	}

	nb.setErr(nil)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	if nb.group(groupA) != nil {
		t.Fatal("the network's group must be deleted")
	}
	if got := nb.portGroup(t, lrpA); got != "" {
		t.Fatalf("router port still points at %q", got)
	}
	if nb.group("px-vpc-y-subnet-z") == nil || nb.group("px-by-hand") == nil {
		t.Fatal("other groups must stay")
	}
	if n := nb.count(tableHAChassis); n != 2 {
		t.Fatalf("%d HA_Chassis rows left, want the 2 of the other groups", n)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the VLAN must be released after the group: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
}

func TestGatewayCollectorDeletesGroupsOfDeletedNetworks(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	reconcileNetWith(t, r, tenantA, "subnet-a")
	nb.addRouterPort("vpc-y-subnet-gone")
	nb.addGroup("px-vpc-y-subnet-gone", map[string]string{"owner": "proxmox-network", "network": "tenant-b/gone"}, map[string]int{"ch-w1": 100}, "vpc-y-subnet-gone")
	nb.addGroup("px-by-hand", nil, map[string]int{"ch-w2": 1})
	nb.addGroup("px-foreign", map[string]string{"owner": "someone", "network": "tenant-b/gone"}, map[string]int{"ch-w2": 1})
	nb.addGroup("px-malformed", map[string]string{"owner": "proxmox-network", "network": "no-slash"}, map[string]int{"ch-w2": 1})

	n, err := r.Gateways.collect(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || nb.group("px-vpc-y-subnet-gone") != nil {
		t.Fatalf("collected %d, want the one group of the deleted network", n)
	}
	if got := nb.portGroup(t, "vpc-y-subnet-gone"); got != "" {
		t.Fatalf("router port still points at %q", got)
	}
	for _, keep := range []string{groupA, "px-by-hand", "px-foreign", "px-malformed"} {
		if nb.group(keep) == nil {
			t.Fatalf("%s must stay", keep)
		}
	}

	// The runnable does the same on its own, on start and then periodically.
	nb.addGroup("px-later", map[string]string{"owner": "proxmox-network", "network": "tenant-b/later"}, map[string]int{"ch-w1": 100})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- (&GatewayCollector{Gateways: r.Gateways, Reader: c, Period: time.Hour}).Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for nb.group("px-later") != nil {
		if time.Now().After(deadline) {
			t.Fatal("the collector did not run on start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !(&GatewayCollector{}).NeedLeaderElection() {
		t.Fatal("collection must run on the leader only")
	}
}

func TestGatewayDisabled(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	r.Gateways = nil
	res := reconcileNetWith(t, r, tenantA, "subnet-a")

	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionUnknown, pxv1.GatewayReasonDisabled)
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if res.RequeueAfter != resyncPeriod {
		t.Fatalf("requeue after %v, want %v", res.RequeueAfter, resyncPeriod)
	}
	if nb.count(tableHAChassisGrp) != 0 {
		t.Fatal("nothing may be written with management off")
	}
	if hasSeries(t, networkGatewayReady, tenantA, "subnet-a") || hasSeries(t, networkGatewayChassis, tenantA, "subnet-a") {
		t.Fatal("no gateway series with management off")
	}

	// Switched on later, the same network gets its group and keeps Ready.
	r.Gateways = &GatewayManager{nb: nb}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionTrue, pxv1.GatewayReasonChassisAssigned)
	if nb.group(groupA) == nil {
		t.Fatal("no group after management was switched on")
	}
}

func TestNodeEventsReachTheNetworksOfTheirProviderNetwork(t *testing.T) {
	c, _, r := gatewayFixture(t)
	other := zone(func(z *pxv1.ProxmoxNetworkZone) { z.Name = "other"; z.Spec.ProviderNetwork = "pxother" })
	if err := c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	w1 := trunkNode("worker1", "ch-w1", true)
	got := r.nodeToNetworks(context.Background(), w1)
	if len(got) != 1 || got[0].Namespace != tenantA || got[0].Name != "subnet-a" {
		t.Fatalf("worker1 maps to %v", got)
	}
	// A node that lost its label still maps through the label's other value.
	notReady := w1.DeepCopy()
	notReady.Labels[providerReadyLabel(provider)] = "false"
	if len(r.nodeToNetworks(context.Background(), notReady)) != 1 {
		t.Fatal("a node with the label at any value maps to its zone's networks")
	}
	if got := r.nodeToNetworks(context.Background(), trunkNode("node1", "ch-n1", false)); len(got) != 0 {
		t.Fatalf("a node off every provider network maps to %v", got)
	}

	update := func(old, new *corev1.Node) bool {
		return nodeGatewayPredicate.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new})
	}
	relabel := w1.DeepCopy()
	delete(relabel.Labels, providerReadyLabel(provider))
	rechassis := w1.DeepCopy()
	rechassis.Annotations[AnnotationChassis] = "ch-other"
	unrelated := w1.DeepCopy()
	unrelated.Labels["topology.kubernetes.io/zone"] = "a"
	unrelated.Annotations["something"] = "else"
	switch {
	case !update(w1, relabel):
		t.Fatal("losing the ready label must trigger")
	case !update(w1, rechassis):
		t.Fatal("a chassis change must trigger")
	case update(w1, unrelated):
		t.Fatal("unrelated label and annotation changes must not trigger")
	case !nodeGatewayPredicate.Create(event.CreateEvent{Object: w1}) || !nodeGatewayPredicate.Delete(event.DeleteEvent{Object: w1}):
		t.Fatal("node create and delete must trigger")
	}
}

// A router port that is already a gateway port of someone else's making is
// not taken over, and no group is made next to it.
func TestGatewayLeavesAForeignRouterPortAlone(t *testing.T) {
	for name, setup := range map[string]func(nb *fakeNB){
		"pointed at a group named the way Kube-OVN names its own": func(nb *fakeNB) {
			nb.addGroup(lrpA, map[string]string{"lrp": lrpA}, map[string]int{"ch-n1": 100}, lrpA)
		},
		"pointed at an unowned group of another name": func(nb *fakeNB) {
			nb.addGroup("by-hand", nil, map[string]int{"ch-n1": 100}, lrpA)
		},
		"with gateway_chassis": func(nb *fakeNB) {
			nb.setPortGatewayChassis(lrpA, "00000000-0000-4000-8000-999999999999")
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, nb, r := gatewayFixture(t)
			setup(nb)
			before := nb.portGroup(t, lrpA)
			reconcileNetWith(t, r, tenantA, "subnet-a")

			if got := nb.portGroup(t, lrpA); got != before {
				t.Fatalf("the router port was moved from %q to %q", before, got)
			}
			if nb.group(groupA) != nil || nb.writeCount() != 0 {
				t.Fatal("nothing may be written for a router port someone else made a gateway port")
			}
			n := getNet(t, c, tenantA, "subnet-a")
			wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonForeignGroup)
			wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, pxv1.GatewayReasonForeignGroup)
			if before != "" && !strings.Contains(cond(n, pxv1.ConditionGatewayReady).Message, before) {
				t.Fatalf("the message must name the group in use: %s", cond(n, pxv1.ConditionGatewayReady).Message)
			}
			if v := gaugeValue(t, networkGatewayChassis.WithLabelValues(tenantA, "subnet-a")); v != 0 {
				t.Fatalf("gateway chassis metric = %v, want 0", v)
			}
		})
	}
}

// A router port that someone makes a gateway port between the read and the
// write aborts the whole write: no pointer is taken and no group is left.
func TestGatewayWriteAbortsWhenTheRouterPortChangesUnderIt(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	h := &hookNB{fakeNB: nb}
	h.before = func(n int) {
		if n == 2 { // create: the read, then the write
			nb.addGroup(lrpA, map[string]string{"lrp": lrpA}, map[string]int{"ch-n1": 100}, lrpA)
		}
	}
	r.Gateways = &GatewayManager{nb: h}
	reconcileNetWith(t, r, tenantA, "subnet-a")

	if nb.waitsFailed != 1 {
		t.Fatalf("%d transactions aborted by a wait, want 1", nb.waitsFailed)
	}
	if got := nb.portGroup(t, lrpA); got != lrpA {
		t.Fatalf("router port points at %q, want it left at %q", got, lrpA)
	}
	if nb.group(groupA) != nil || nb.count(tableHAChassis) != 1 {
		t.Fatal("the aborted write must leave no group and no HA_Chassis of ours")
	}
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)

	// The next pass sees the foreign group.
	h.before = nil
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonForeignGroup)
}

// A group deleted between the read and the write leaves every update of
// the write matching nothing; that must not read as ChassisAssigned.
func TestGatewayWriteThatMatchesNothingIsAConflict(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	ours := map[string]string{"owner": "proxmox-network", "network": "tenant-a/subnet-a"}
	nb.addGroup(groupA, ours, map[string]int{"ch-w1": 20, "ch-w2": 10}, lrpA)
	h := &hookNB{fakeNB: nb}
	h.before = func(n int) {
		if n == 3 { // the group, its chassis, then the write
			nb.dropGroup(groupA)
		}
	}
	r.Gateways = &GatewayManager{nb: h}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	if nb.group(groupA) != nil {
		t.Fatal("the test expects the group gone")
	}

	h.before = nil
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionTrue, pxv1.GatewayReasonChassisAssigned)
	if nb.portGroup(t, lrpA) != groupA {
		t.Fatal("the next pass must make the group again")
	}
}

// Two HA_Chassis rows for one chassis, as a hand-made group may have: one
// goes, and the group holds each chassis once.
func TestGatewayDropsDuplicateChassisRows(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	nb.addGroupRows(groupA, nil, []gatewayChassis{{"ch-w1", 20}, {"ch-w1", 15}, {"ch-w2", 10}}, lrpA)
	reconcileNetWith(t, r, tenantA, "subnet-a")

	if n := nb.count(tableHAChassis); n != 2 {
		t.Fatalf("%d HA_Chassis rows, want one per chassis", n)
	}
	if want := wantedChassis(t, c, tenantA, "subnet-a"); !maps.Equal(nb.group(groupA).Chassis, want) {
		t.Fatalf("chassis = %v, want %v", nb.group(groupA).Chassis, want)
	}
}

// The router port does not exist yet: retried soon, and the chassis metric
// reads 0 even when a group of ours is still there.
func TestGatewayRouterPortMissingIsRetriedSoon(t *testing.T) {
	_, nb, r := gatewayFixture(t)
	nb.addGroup(groupA, map[string]string{"owner": "proxmox-network", "network": "tenant-a/subnet-a"}, map[string]int{"ch-w1": 100, "ch-w2": 90})
	nb.deleteRouterPort(lrpA)
	res := reconcileNetWith(t, r, tenantA, "subnet-a")
	if res.RequeueAfter != gatewayRetryPeriod {
		t.Fatalf("requeue after %v, want %v", res.RequeueAfter, gatewayRetryPeriod)
	}
	if v := gaugeValue(t, networkGatewayChassis.WithLabelValues(tenantA, "subnet-a")); v != 0 {
		t.Fatalf("gateway chassis metric = %v, want 0 without a router port", v)
	}
}

// The group stays while the network is in use or its subnet still exists:
// the VMs on it still need their gateway.
func TestGatewayIsKeptWhileTheNetworkIsStillUsed(t *testing.T) {
	for name, keep := range map[string]func(t *testing.T, c client.Client){
		"addresses allocated": func(t *testing.T, c client.Client) {
			p := &InClusterIPPool{}
			_ = c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, p)
			p.Status.Addresses.Used, p.Status.Addresses.Free = 3, 97
			if err := c.Status().Update(context.Background(), p); err != nil {
				if err := c.Update(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
		},
		"subnet still exists": func(*testing.T, client.Client) {},
	} {
		t.Run(name, func(t *testing.T) {
			c, nb, r := gatewayFixture(t)
			reconcileNetWith(t, r, tenantA, "subnet-a")
			keep(t, c)
			deleteNet(t, c, tenantA, "subnet-a")
			for range 2 {
				reconcileNetWith(t, r, tenantA, "subnet-a")
			}
			if nb.group(groupA) == nil || nb.portGroup(t, lrpA) != groupA {
				t.Fatal("the group must stay while the network may still carry VMs")
			}
			wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "")
		})
	}
}

// A network that never had a VLAN never had a group: its deletion does not
// wait for the NB.
func TestGatewayReleaseIsSkippedWithoutAVLAN(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	z := &pxv1.ProxmoxNetworkZone{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: zoneName}, z)
	if err := c.Delete(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	if getNet(t, c, tenantA, "subnet-a").Status.VLAN != 0 {
		t.Fatal("the test needs a network without a VLAN")
	}
	nb.setErr(errors.New("connection refused"))
	deleteNet(t, c, tenantA, "subnet-a")
	deleteSubnet(t, c, "subnet-a")
	for range 2 {
		reconcileNetWith(t, r, tenantA, "subnet-a")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the network must go without the NB: %v", err)
	}
}

// failingReader fails every read the way an API server under load does.
type failingReader struct{ err error }

func (f failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return f.err
}

func (f failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return f.err
}

// Only NotFound means a network is gone: any other error from the API keeps
// every group.
func TestGatewayCollectorKeepsGroupsWhenTheAPIFails(t *testing.T) {
	for _, apiErr := range []error{
		apierrors.NewServiceUnavailable("etcd leader changed"),
		apierrors.NewForbidden(pxv1.GroupVersion.WithResource("proxmoxnetworks").GroupResource(), "subnet-a", errors.New("rbac")),
	} {
		_, nb, r := gatewayFixture(t)
		nb.addGroup("px-vpc-y-subnet-b", map[string]string{"owner": "proxmox-network", "network": "tenant-b/subnet-b"}, map[string]int{"ch-w1": 100})
		n, err := r.Gateways.collect(context.Background(), failingReader{apiErr})
		if err == nil || n != 0 {
			t.Fatalf("collect = %d, %v; want an error and nothing collected", n, err)
		}
		if nb.group("px-vpc-y-subnet-b") == nil {
			t.Fatalf("a group was deleted on %v", apiErr)
		}
	}
}

// Switching management on must not leave GatewayReady at Disabled when the
// pass stops before the gateway step.
func TestGatewayDisabledDoesNotOutliveManagement(t *testing.T) {
	c, nb, r := gatewayFixture(t)
	r.Gateways = nil
	reconcileNetWith(t, r, tenantA, "subnet-a")
	z := &pxv1.ProxmoxNetworkZone{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: zoneName}, z)
	if err := c.Delete(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	r.Gateways = &GatewayManager{nb: nb}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionGatewayReady, metav1.ConditionUnknown, "WaitingForVLAN")
}

func TestNodeDeletionStartTriggers(t *testing.T) {
	w1 := trunkNode("worker1", "ch-w1", true)
	deleting := w1.DeepCopy()
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	if !nodeGatewayPredicate.Update(event.UpdateEvent{ObjectOld: w1, ObjectNew: deleting}) {
		t.Fatal("a node that starts deleting leaves the groups and must trigger")
	}
}

// stuckNB never answers before the caller's context ends, like an NB behind
// a black hole.
type stuckNB struct{}

func (stuckNB) Transact(ctx context.Context, _ ...ovsdbOp) ([]ovsdbResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(3 * time.Second):
		return nil, errors.New("the pass was not bounded")
	}
}

// An NB that does not answer holds a pass, and with it the controller's
// single worker, for the pass timeout only, on reconcile and on delete.
func TestGatewayPassIsBounded(t *testing.T) {
	c, _, r := gatewayFixture(t)
	r.Gateways = &GatewayManager{nb: stuckNB{}, passTimeout: 50 * time.Millisecond}
	start := time.Now()
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionGatewayReady, metav1.ConditionFalse, pxv1.GatewayReasonNBUnavailable)
	if !strings.Contains(cond(n, pxv1.ConditionGatewayReady).Message, "deadline exceeded") {
		t.Fatalf("message = %s", cond(n, pxv1.ConditionGatewayReady).Message)
	}

	deleteNet(t, c, tenantA, "subnet-a")
	deleteSubnet(t, c, "subnet-a")
	for range 2 {
		reconcileNetWith(t, r, tenantA, "subnet-a")
	}
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "GatewayReleasePending")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("passes took %v with a 50ms pass timeout", d)
	}
}
