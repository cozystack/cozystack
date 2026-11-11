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

// The VPC gateway of a Proxmox network.
//
// A VM on a Proxmox VLAN reaches its subnet's VPC router port only through
// the logical switch's localnet port. Kube-OVN builds OVN with its patch
// "northd: skip arp/nd request for lrp addresses from localnet ports"
// (477695a0), which installs, for every logical router port that has neither
// gateway_chassis nor ha_chassis_group, a ls_in_arp_rsp flow that drops ARP
// and ND requests for the port's addresses arriving from a localnet port. So
// the VMs get no answer for their gateway. Making the router port a
// distributed gateway port, by giving it an HA_Chassis_Group, removes that
// drop: the active chassis of the group answers and routes for the VLAN.
//
// Kube-OVN has no API for this on a subnet's router port, so the controller
// writes it to the OVN northbound database itself, and touches nothing else:
//
//   - one HA_Chassis_Group per network, named "px-<router port>", with
//     external_ids owner=proxmox-network and network=<namespace>/<name>;
//   - its HA_Chassis rows, one per trunk node of the zone's provider network;
//   - the ha_chassis_group column of the subnet's router port.
//
// A group of that name without an owner is adopted (groups made by hand
// before the controller managed them); a group with any other owner, or owned
// for another network, is left alone and reported, and so is a router port
// that already uses another group or has gateway_chassis: should Kube-OVN one
// day manage these ports itself, the two must not take the port from each
// other. This is meant to last only until Kube-OVN sets the group itself.

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

const (
	tableLRP           = "Logical_Router_Port"
	tableHAChassisGrp  = "HA_Chassis_Group"
	tableHAChassis     = "HA_Chassis"
	colUUID            = "_uuid"
	colName            = "name"
	colHAChassisGroup  = "ha_chassis_group"
	colGatewayChassis  = "gateway_chassis"
	colHAChassis       = "ha_chassis"
	colChassisName     = "chassis_name"
	colPriority        = "priority"
	colExternalIDs     = "external_ids"
	namedGroup         = "pxgroup"
	namedChassisPrefix = "pxchassis"

	// AnnotationChassis is Kube-OVN's node annotation with the node's OVN
	// chassis name.
	AnnotationChassis = "ovn.kubernetes.io/chassis"
	// providerReadyLabelSuffix follows "<provider network>" in the label
	// kube-ovn-cni sets to "true" on a node once the provider network's NIC
	// is attached there.
	providerReadyLabelSuffix = ".provider-network.kubernetes.io/ready"

	// gatewayRetryPeriod is how soon a network is retried after the NB could
	// not be reached, or while its router port does not exist yet.
	gatewayRetryPeriod = 30 * time.Second
	// gatewayNBGracePeriod is how long a network whose gateway was up keeps
	// GatewayReady, and with it Ready, while the NB cannot be reached. The
	// group stays in the NB through a leader election or an API blip, and
	// the VMs keep their gateway; charts that render machines only on a
	// Ready network should not fail for that.
	gatewayNBGracePeriod = 10 * time.Minute
	// gatewayPassTimeout bounds the NB work of one network's pass, so an NB
	// that does not answer cannot hold the controller's single worker.
	gatewayPassTimeout = 5 * time.Second
	// gatewayResyncPeriod repairs groups someone cleared and router ports
	// Kube-OVN recreated without one.
	gatewayResyncPeriod = 5 * time.Minute
	// DefaultGatewayCollectPeriod is how often groups of deleted networks
	// are collected.
	DefaultGatewayCollectPeriod = 5 * time.Minute
)

func providerReadyLabel(provider string) string {
	return provider + providerReadyLabelSuffix
}

// routerPortName is Kube-OVN's name for a VPC subnet's router port.
func routerPortName(vpc, subnet string) string {
	return vpc + "-" + subnet
}

func gatewayGroupName(lrp string) string {
	return pxv1.GatewayGroupPrefix + lrp
}

func networkKey(namespace, name string) string {
	return namespace + "/" + name
}

// gatewayChassis is one HA_Chassis of a group.
type gatewayChassis struct {
	Name     string
	Priority int
}

// gatewayKeyForVPC is the rendezvous key of every network of one VPC. All
// Proxmox router ports of a VPC share their order, and so their active
// chassis: a VM's packet to another subnet of its VPC, or to the egress
// gateway on the VPC's internal subnet, then leaves the router on the chassis
// it entered and goes out through a localnet port. Ports of one VPC on
// different chassis would send it through the Geneve tunnel instead, where
// Cilium's kube-proxy replacement (bpf_host on genev_sys_6081) rewrites a
// LoadBalancer IP to a pod address the VPC cannot reach.
func gatewayKeyForVPC(vpc string) string { return "vpc/" + vpc }

// gatewayChassisFor orders the trunk nodes that have a chassis for one key
// (gatewayKeyForVPC) by rendezvous hashing: each node scores a hash of the
// key and the node's name, and the highest score gets the highest priority,
// and with it the active gateway. Different VPCs so land on different nodes,
// and a change of the trunk nodes moves only the VPCs it has to: a new node
// becomes active only for the VPCs it outscores every other node on, about
// 1/n of them, and a node that leaves moves only the VPCs it was active for;
// the order of the other nodes stays as it was. Priorities run 100, 90, 80,
// ... (from 10 per node for more than ten nodes), all distinct, so OVN never
// has to break a tie.
func gatewayChassisFor(nodes []corev1.Node, key string) []gatewayChassis {
	seen := map[string]bool{}
	type entry struct {
		node, chassis string
		score         uint64
	}
	// By name, so that of two nodes reporting one chassis the same one
	// counts whatever order they are listed in.
	sorted := slices.Clone(nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var list []entry
	for _, n := range sorted {
		c := strings.TrimSpace(n.Annotations[AnnotationChassis])
		if c == "" || seen[c] || !n.DeletionTimestamp.IsZero() {
			continue
		}
		seen[c] = true
		list = append(list, entry{n.Name, c, rendezvousScore(key, n.Name)})
	}
	if len(list) == 0 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].node < list[j].node
	})
	top := max(100, 10*len(list))
	out := make([]gatewayChassis, 0, len(list))
	for i, e := range list {
		out = append(out, gatewayChassis{Name: e.chassis, Priority: top - 10*i})
	}
	return out
}

// rendezvousScore is the weight of a node for a network: FNV-1a of both,
// finished with the splitmix64 mixer, because FNV alone leaves keys that
// differ only in their last bytes too close together to rank fairly.
func rendezvousScore(network, node string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(network))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(node))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// +kubebuilder:object:generate=false

// GatewayManager keeps the HA chassis groups of the networks' router ports in
// the OVN northbound database.
type GatewayManager struct {
	nb ovsdbTransactor
	// passTimeout overrides gatewayPassTimeout when set.
	passTimeout time.Duration

	mu sync.Mutex
	// failingSince is when the NB first failed for a network, for each
	// network whose last pass failed (gatewayNBGracePeriod).
	failingSince map[string]time.Time
}

// NewGatewayManager returns a manager that writes through nb.
func NewGatewayManager(nb *NBClient) *GatewayManager {
	return &GatewayManager{nb: nb}
}

// withPassTimeout bounds the NB work of one pass.
func (m *GatewayManager) withPassTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := m.passTimeout
	if d <= 0 {
		d = gatewayPassTimeout
	}
	return context.WithTimeout(ctx, d)
}

// noteFailure records a failed pass for the network and returns when its
// run of failures began.
func (m *GatewayManager) noteFailure(network string, now time.Time) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failingSince == nil {
		m.failingSince = map[string]time.Time{}
	}
	since, ok := m.failingSince[network]
	if !ok {
		since = now
		m.failingSince[network] = since
	}
	return since
}

// noteSuccess ends the network's run of failures.
func (m *GatewayManager) noteSuccess(network string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.failingSince, network)
}

// failing reports whether the network's last pass could not reach the NB.
func (m *GatewayManager) failing(network string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.failingSince[network]
	return ok
}

// gatewayState is what one pass found and left.
type gatewayState struct {
	Reason  string
	Message string
	// Chassis is the number of HA_Chassis in the group after the pass.
	Chassis int
}

type groupRow struct {
	UUID        string
	Name        string
	ExternalIDs map[string]string
	Chassis     []string
}

func parseGroup(r ovsdbRow) groupRow {
	return groupRow{UUID: r.uuid(colUUID), Name: r.str(colName), ExternalIDs: r.strMap(colExternalIDs), Chassis: r.uuids(colHAChassis)}
}

// claim says whether the controller may write the group for network, and
// why not.
func (g groupRow) claim(network string) (bool, string) {
	owner := g.ExternalIDs[pxv1.GatewayExternalIDOwner]
	switch {
	case owner == "":
		return true, ""
	case owner != pxv1.GatewayOwner:
		return false, fmt.Sprintf("OVN HA_Chassis_Group %q belongs to %q", g.Name, owner)
	case g.ExternalIDs[pxv1.GatewayExternalIDNetwork] != network:
		return false, fmt.Sprintf("OVN HA_Chassis_Group %q belongs to network %q", g.Name, g.ExternalIDs[pxv1.GatewayExternalIDNetwork])
	}
	return true, ""
}

func (m *GatewayManager) transact(ctx context.Context, ops ...ovsdbOp) ([]ovsdbResult, error) {
	res, err := m.nb.Transact(ctx, ops...)
	if err != nil {
		return nil, err
	}
	if err := checkResults(ops, res); err != nil {
		return nil, err
	}
	return res, nil
}

// ensure makes the group of the router port hold exactly want and the router
// port point at it. It writes nothing when the router port is missing, the
// group or the router port belongs to someone else, or want is empty.
func (m *GatewayManager) ensure(ctx context.Context, lrp, network string, want []gatewayChassis) (gatewayState, error) {
	group := gatewayGroupName(lrp)
	res, err := m.transact(ctx,
		ovsdbOp{Op: "select", Table: tableLRP, Where: []ovsdbCondition{ovsdbWhere(colName, "==", lrp)},
			Columns: []string{colUUID, colHAChassisGroup, colGatewayChassis}},
		ovsdbOp{Op: "select", Table: tableHAChassisGrp, Where: []ovsdbCondition{ovsdbWhere(colName, "==", group)},
			Columns: []string{colUUID, colName, colExternalIDs, colHAChassis}},
	)
	if err != nil {
		return gatewayState{}, err
	}
	var existing *groupRow
	if rows := res[1].Rows; len(rows) > 0 {
		g := parseGroup(rows[0])
		existing = &g
	}
	st := gatewayState{}
	if existing != nil {
		if ok, why := existing.claim(network); !ok {
			st.Reason, st.Message = pxv1.GatewayReasonForeignGroup, why+"; the controller leaves it alone"
			return st, nil
		}
	}
	if len(res[0].Rows) == 0 {
		st.Reason, st.Message = pxv1.GatewayReasonRouterPortMissing, fmt.Sprintf("OVN has no logical router port %q yet", lrp)
		return st, nil
	}
	port := res[0].Rows[0]
	portUUID, portGroup := port.uuid(colUUID), port.uuid(colHAChassisGroup)
	// A router port that is already a gateway port of someone else's making
	// is not taken over: whoever set it would set it back, and the two
	// would take the port from each other on every pass.
	if gc := port.uuids(colGatewayChassis); len(gc) > 0 {
		st.Reason, st.Message = pxv1.GatewayReasonForeignGroup,
			fmt.Sprintf("router port %q has %d gateway_chassis; the controller leaves it alone", lrp, len(gc))
		return st, nil
	}
	if portGroup != "" && (existing == nil || portGroup != existing.UUID) {
		st.Reason, st.Message = pxv1.GatewayReasonForeignGroup,
			fmt.Sprintf("router port %q already uses %s; the controller leaves it alone", lrp, m.describeGroup(ctx, portGroup))
		return st, nil
	}
	if existing != nil {
		st.Chassis = len(existing.Chassis)
	}
	if len(want) == 0 {
		st.Reason, st.Message = pxv1.GatewayReasonNoTrunkNodes, "no node of the zone's provider network is ready with an OVN chassis"
		return st, nil
	}

	// The chassis rows the group holds now, by chassis name.
	type chassisRow struct {
		uuid     string
		priority int
	}
	have := map[string]chassisRow{}
	var drop []any
	if existing != nil && len(existing.Chassis) > 0 {
		sel := make([]ovsdbOp, 0, len(existing.Chassis))
		for _, u := range existing.Chassis {
			sel = append(sel, ovsdbOp{Op: "select", Table: tableHAChassis,
				Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(u))}, Columns: []string{colUUID, colChassisName, colPriority}})
		}
		res, err := m.transact(ctx, sel...)
		if err != nil {
			return gatewayState{}, err
		}
		for i, r := range res {
			if len(r.Rows) == 0 {
				// Referenced but gone: a dangling reference ovsdb-server
				// would not allow. Drop it all the same.
				drop = append(drop, ovsUUID(existing.Chassis[i]))
				continue
			}
			row := r.Rows[0]
			name := row.str(colChassisName)
			if _, dup := have[name]; dup {
				drop = append(drop, ovsUUID(row.uuid(colUUID)))
				continue
			}
			have[name] = chassisRow{uuid: row.uuid(colUUID), priority: row.integer(colPriority)}
		}
	}

	var ops []ovsdbOp
	var add []any
	wanted := map[string]bool{}
	for i, c := range want {
		wanted[c.Name] = true
		if h, ok := have[c.Name]; ok {
			if h.priority != c.Priority {
				ops = append(ops, ovsdbOp{Op: "update", Table: tableHAChassis,
					Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(h.uuid))},
					Row:   map[string]any{colPriority: c.Priority}})
			}
			continue
		}
		ref := fmt.Sprintf("%s%d", namedChassisPrefix, i)
		ops = append(ops, ovsdbOp{Op: "insert", Table: tableHAChassis, UUIDName: ref,
			Row: map[string]any{colChassisName: c.Name, colPriority: c.Priority}})
		add = append(add, ovsNamedUUID(ref))
	}
	for name, h := range have {
		if !wanted[name] {
			drop = append(drop, ovsUUID(h.uuid))
		}
	}
	sort.Slice(drop, func(i, j int) bool { return drop[i].(ovsUUID) < drop[j].(ovsUUID) })

	ids := map[string]string{pxv1.GatewayExternalIDOwner: pxv1.GatewayOwner, pxv1.GatewayExternalIDNetwork: network}
	var groupRef any
	adopted := false
	if existing == nil {
		ops = append(ops, ovsdbOp{Op: "insert", Table: tableHAChassisGrp, UUIDName: namedGroup,
			Row: map[string]any{colName: group, colExternalIDs: ovsMap(ids), colHAChassis: ovsSet(add)}})
		groupRef = ovsNamedUUID(namedGroup)
	} else {
		groupRef = ovsUUID(existing.UUID)
		where := []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(existing.UUID))}
		if len(drop) > 0 {
			ops = append(ops, ovsdbOp{Op: "mutate", Table: tableHAChassisGrp, Where: where,
				Mutations: []ovsdbMutation{ovsdbMutate(colHAChassis, "delete", ovsSet(drop))}})
		}
		if len(add) > 0 {
			ops = append(ops, ovsdbOp{Op: "mutate", Table: tableHAChassisGrp, Where: where,
				Mutations: []ovsdbMutation{ovsdbMutate(colHAChassis, "insert", ovsSet(add))}})
		}
		if existing.ExternalIDs[pxv1.GatewayExternalIDOwner] != pxv1.GatewayOwner ||
			existing.ExternalIDs[pxv1.GatewayExternalIDNetwork] != network {
			merged := ovsMap{}
			for k, v := range existing.ExternalIDs {
				merged[k] = v
			}
			for k, v := range ids {
				merged[k] = v
			}
			ops = append(ops, ovsdbOp{Op: "update", Table: tableHAChassisGrp, Where: where,
				Row: map[string]any{colExternalIDs: merged}})
			adopted = true
		}
	}
	if portGroup == "" {
		// Only a router port that is still no gateway port is pointed at
		// the group. The wait aborts the whole transaction, the group's
		// insert included, if someone else made it a gateway port since the
		// read; the pass then fails and the next one finds that out.
		where := []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(portUUID))}
		ops = append([]ovsdbOp{{Op: "wait", Table: tableLRP, Where: where,
			Columns: []string{colHAChassisGroup, colGatewayChassis}, Until: "==",
			WaitRows: []map[string]any{{colHAChassisGroup: ovsSet{}, colGatewayChassis: ovsSet{}}}}}, ops...)
		ops = append(ops, ovsdbOp{Op: "update", Table: tableLRP, Where: where,
			Row: map[string]any{colHAChassisGroup: groupRef}})
	}

	st.Reason = pxv1.GatewayReasonChassisAssigned
	st.Chassis = len(want)
	st.Message = gatewayMessage(group, lrp, want)
	if len(ops) == 0 {
		return st, nil
	}
	res, err = m.transact(ctx, ops...)
	if err != nil {
		return gatewayState{}, err
	}
	// Every write names its row by _uuid; one that matched nothing raced
	// with someone else, and the next pass reads again.
	for i, op := range ops {
		if (op.Op == "update" || op.Op == "mutate") && res[i].Count != 1 {
			return gatewayState{}, fmt.Errorf("OVN NB row of %s changed under the controller (%s matched %d rows)", op.Table, op.Op, res[i].Count)
		}
	}
	log.FromContext(ctx).Info("updated OVN HA chassis group", "group", group, "routerPort", lrp,
		"chassis", st.Message, "created", existing == nil, "adopted", adopted)
	return st, nil
}

// describeGroup names the group with that uuid and its owner, for a message.
func (m *GatewayManager) describeGroup(ctx context.Context, uuid string) string {
	res, err := m.transact(ctx, ovsdbOp{Op: "select", Table: tableHAChassisGrp,
		Where:   []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(uuid))},
		Columns: []string{colName, colExternalIDs}})
	if err != nil || len(res[0].Rows) == 0 {
		return fmt.Sprintf("HA_Chassis_Group %s", uuid)
	}
	g := parseGroup(res[0].Rows[0])
	if owner := g.ExternalIDs[pxv1.GatewayExternalIDOwner]; owner != "" {
		return fmt.Sprintf("HA_Chassis_Group %q of %q", g.Name, owner)
	}
	return fmt.Sprintf("HA_Chassis_Group %q", g.Name)
}

func gatewayMessage(group, lrp string, want []gatewayChassis) string {
	parts := make([]string, 0, len(want))
	for _, c := range want {
		parts = append(parts, fmt.Sprintf("%s=%d", c.Name, c.Priority))
	}
	return fmt.Sprintf("router port %q has HA chassis group %q with %d chassis (%s); the highest priority is active",
		lrp, group, len(want), strings.Join(parts, ", "))
}

// ownedGroups lists the groups the controller owns, those of one network when
// network is set.
func (m *GatewayManager) ownedGroups(ctx context.Context, network string) ([]groupRow, error) {
	ids := ovsMap{pxv1.GatewayExternalIDOwner: pxv1.GatewayOwner}
	if network != "" {
		ids[pxv1.GatewayExternalIDNetwork] = network
	}
	res, err := m.transact(ctx, ovsdbOp{Op: "select", Table: tableHAChassisGrp,
		Where:   []ovsdbCondition{ovsdbWhere(colExternalIDs, "includes", ids)},
		Columns: []string{colUUID, colName, colExternalIDs, colHAChassis}})
	if err != nil {
		return nil, err
	}
	out := make([]groupRow, 0, len(res[0].Rows))
	for _, r := range res[0].Rows {
		out = append(out, parseGroup(r))
	}
	return out, nil
}

// deleteGroup clears the group from every router port that points at it and
// deletes it, in one transaction; its HA_Chassis rows go with it.
func (m *GatewayManager) deleteGroup(ctx context.Context, g groupRow) error {
	res, err := m.transact(ctx, ovsdbOp{Op: "select", Table: tableLRP,
		Where:   []ovsdbCondition{ovsdbWhere(colHAChassisGroup, "includes", ovsUUID(g.UUID))},
		Columns: []string{colUUID, colName}})
	if err != nil {
		return err
	}
	var ops []ovsdbOp
	var ports []string
	for _, r := range res[0].Rows {
		ports = append(ports, r.str(colName))
		ops = append(ops, ovsdbOp{Op: "update", Table: tableLRP,
			Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(r.uuid(colUUID)))},
			Row:   map[string]any{colHAChassisGroup: ovsSet{}}})
	}
	ops = append(ops, ovsdbOp{Op: "delete", Table: tableHAChassisGrp,
		Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID(g.UUID))}})
	if _, err := m.transact(ctx, ops...); err != nil {
		return err
	}
	log.FromContext(ctx).Info("deleted OVN HA chassis group", "group", g.Name,
		"network", g.ExternalIDs[pxv1.GatewayExternalIDNetwork], "clearedRouterPorts", ports)
	return nil
}

// release deletes every group the controller owns for the network.
func (m *GatewayManager) release(ctx context.Context, network string) error {
	groups, err := m.ownedGroups(ctx, network)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if err := m.deleteGroup(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

// collect deletes the owned groups whose network no longer exists.
func (m *GatewayManager) collect(ctx context.Context, reader client.Reader) (int, error) {
	groups, err := m.ownedGroups(ctx, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range groups {
		ns, name, ok := strings.Cut(g.ExternalIDs[pxv1.GatewayExternalIDNetwork], "/")
		if !ok || ns == "" || name == "" {
			log.FromContext(ctx).Info("skipping an owned OVN HA chassis group without a valid network", "group", g.Name)
			continue
		}
		err := reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &pxv1.ProxmoxNetwork{})
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return n, err
		}
		if err := m.deleteGroup(ctx, g); err != nil {
			return n, err
		}
		gatewayGroupsCollected.Inc()
		n++
	}
	return n, nil
}

// +kubebuilder:object:generate=false

// GatewayCollector deletes, periodically and on the leader only, the HA
// chassis groups the controller owns whose ProxmoxNetwork is gone: a network
// whose finalizer was removed by hand, or one deleted while the controller
// did not manage gateways.
type GatewayCollector struct {
	Gateways *GatewayManager
	// Reader must read past the cache: a network deleted a moment ago must
	// not keep its group, and one created a moment ago must not lose it.
	Reader client.Reader
	Period time.Duration
}

// Start runs the collection until ctx ends.
func (c *GatewayCollector) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("gateway-collector")
	ctx = log.IntoContext(ctx, logger)
	period := c.Period
	if period <= 0 {
		period = DefaultGatewayCollectPeriod
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		if n, err := c.Gateways.collect(ctx, c.Reader); err != nil {
			logger.Error(err, "collect OVN HA chassis groups of deleted networks", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// NeedLeaderElection keeps the collection on the leader.
func (c *GatewayCollector) NeedLeaderElection() bool { return true }

// nodeGatewayKey is what of a node decides the networks' chassis: the
// provider networks it is ready on, and its chassis.
func nodeGatewayKey(o client.Object) string {
	var ready []string
	for k, v := range o.GetLabels() {
		if strings.HasSuffix(k, providerReadyLabelSuffix) {
			ready = append(ready, k+"="+v)
		}
	}
	sort.Strings(ready)
	return strings.Join(ready, ",") + "|" + o.GetAnnotations()[AnnotationChassis]
}

// nodeGatewayPredicate passes node events that can change a group.
var nodeGatewayPredicate = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return true },
	DeleteFunc:  func(event.DeleteEvent) bool { return true },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		return nodeGatewayKey(e.ObjectOld) != nodeGatewayKey(e.ObjectNew) ||
			e.ObjectOld.GetDeletionTimestamp().IsZero() != e.ObjectNew.GetDeletionTimestamp().IsZero()
	},
}

// nodeToNetworks maps a node to the networks of every zone whose provider
// network the node carries a ready label of, whatever its value. Update
// events are mapped for the old and the new node, so a node that lost the
// label still reaches its networks.
func (r *NetworkReconciler) nodeToNetworks(ctx context.Context, o client.Object) []reconcile.Request {
	providers := map[string]bool{}
	for k := range o.GetLabels() {
		if p, ok := strings.CutSuffix(k, providerReadyLabelSuffix); ok && p != "" {
			providers[p] = true
		}
	}
	if len(providers) == 0 {
		return nil
	}
	var zones pxv1.ProxmoxNetworkZoneList
	if err := r.List(ctx, &zones); err != nil {
		log.FromContext(ctx).Error(err, "list zones for a node event")
		return nil
	}
	var out []reconcile.Request
	for _, z := range zones.Items {
		if providers[z.Spec.ProviderNetwork] {
			out = append(out, r.zoneToNetworks(ctx, &z)...)
		}
	}
	return out
}
