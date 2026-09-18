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

package endpointattachment

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

// The datapath-delegation contract. The rendered Service is one fixed
// interface with two implementors (cozy-proxy today, cozyplane's proxy
// later); both match these strings literally, so they are rendered exactly
// and nothing is added beside them.
const (
	ServiceProxyNameLabel = "service.kubernetes.io/service-proxy-name"
	ServiceProxyName      = "cozy-proxy"
	WholeIPAnnotation     = "networking.cozystack.io/wholeIP"
	AllowICMPAnnotation   = "networking.cozystack.io/allowICMP"

	// SentinelPort is the placeholder port a whole-IP Service carries when
	// no port list is given, and the one port a not-yet-exposed VM Service
	// has, which makes it "nothing to mirror".
	SentinelPort int32 = 65535

	// OwnerLabel indexes objects this controller renders for an attachment.
	// It is a diagnostic; identity is the controller owner reference.
	OwnerLabel = "cozystack.io/endpoint-attachment"

	// darkSelectorKey is a label no pod carries. A Service whose selector is
	// this label alone keeps its identity and its address while serving
	// nothing, which is what a whole-IP attachment does while its endpoint
	// resolves to more than one backend.
	darkSelectorKey = "internal.cozystack.io/endpoint-attachment-dark"
)

// renderInput is everything the desired Service depends on.
type renderInput struct {
	Endpoint          *corev1.Service
	ClaimName         string
	LoadBalancerClass string
	Dark              bool
	AllocateNodePorts bool
}

// mirroredPorts derives the rendered Service's ports from the endpoint's and
// the member's port list. A nil result with a nil error means nothing
// meaningful is publishable.
func mirroredPorts(endpoint *corev1.Service, lb *cozyv1alpha1.LoadBalancerAttachment) []corev1.ServicePort {
	sentinelOnly := isSentinelOnly(endpoint)
	if len(lb.Ports) == 0 {
		if sentinelOnly {
			if lb.Method == cozyv1alpha1.ExposureMethodWholeIP {
				return []corev1.ServicePort{{Port: SentinelPort}}
			}
			return nil
		}
		return copyPorts(endpoint.Spec.Ports)
	}
	wanted := make([]int32, len(lb.Ports))
	copy(wanted, lb.Ports)
	sort.Slice(wanted, func(i, j int) bool { return wanted[i] < wanted[j] })
	if sentinelOnly {
		out := make([]corev1.ServicePort, 0, len(wanted))
		for _, p := range wanted {
			out = append(out, corev1.ServicePort{
				Name:       fmt.Sprintf("port-%d", p),
				Protocol:   corev1.ProtocolTCP,
				Port:       p,
				TargetPort: intstr.FromInt32(p),
			})
		}
		return out
	}
	set := map[int32]bool{}
	for _, p := range wanted {
		set[p] = true
	}
	var out []corev1.ServicePort
	for _, p := range copyPorts(endpoint.Spec.Ports) {
		if set[p.Port] {
			out = append(out, p)
		}
	}
	return out
}

func isSentinelOnly(svc *corev1.Service) bool {
	if len(svc.Spec.Ports) == 0 {
		return true
	}
	return len(svc.Spec.Ports) == 1 && svc.Spec.Ports[0].Port == SentinelPort
}

func copyPorts(in []corev1.ServicePort) []corev1.ServicePort {
	out := make([]corev1.ServicePort, 0, len(in))
	for _, p := range in {
		out = append(out, corev1.ServicePort{
			Name:        p.Name,
			Protocol:    p.Protocol,
			AppProtocol: p.AppProtocol,
			Port:        p.Port,
			TargetPort:  p.TargetPort,
		})
	}
	return out
}

// desiredService builds the additive LoadBalancer Service for an attachment.
func desiredService(ea *cozyv1alpha1.EndpointAttachment, in renderInput, ports []corev1.ServicePort) *corev1.Service {
	lb := ea.Spec.LoadBalancer
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    ea.Namespace,
			GenerateName: ea.Name + "-",
			Labels:       map[string]string{OwnerLabel: ea.Name},
			Annotations:  map[string]string{ClaimAnnotation: in.ClaimName},
			OwnerReferences: []metav1.OwnerReference{
				controllerOwnerReference(ea),
			},
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeLoadBalancer,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			Ports:                 ports,
			Selector:              copyMap(in.Endpoint.Spec.Selector),
		},
	}
	if in.LoadBalancerClass != "" {
		svc.Spec.LoadBalancerClass = new(in.LoadBalancerClass)
	}
	if !in.AllocateNodePorts {
		svc.Spec.AllocateLoadBalancerNodePorts = new(false)
	}
	if in.Dark {
		svc.Spec.Selector = map[string]string{darkSelectorKey: string(ea.UID)}
	}
	if lb.Method != "" {
		svc.Labels[ServiceProxyNameLabel] = ServiceProxyName
		switch lb.Method {
		case cozyv1alpha1.ExposureMethodWholeIP:
			svc.Annotations[WholeIPAnnotation] = "true"
		case cozyv1alpha1.ExposureMethodPortList:
			svc.Annotations[WholeIPAnnotation] = "false"
			allow := lb.AllowICMP == nil || *lb.AllowICMP
			svc.Annotations[AllowICMPAnnotation] = fmt.Sprintf("%t", allow)
		}
	}
	return svc
}

// applyDesired copies the fields this controller owns onto a live Service,
// leaving everything else (other controllers' annotations, node ports,
// cluster IPs, the immutable loadBalancerClass) untouched. It reports whether
// anything changed.
func applyDesired(live, desired *corev1.Service) bool {
	changed := false
	if live.Labels == nil {
		live.Labels = map[string]string{}
	}
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	for _, k := range []string{OwnerLabel, ServiceProxyNameLabel} {
		if v, ok := desired.Labels[k]; ok {
			if live.Labels[k] != v {
				live.Labels[k] = v
				changed = true
			}
		} else if _, ok := live.Labels[k]; ok {
			delete(live.Labels, k)
			changed = true
		}
	}
	for _, k := range []string{ClaimAnnotation, WholeIPAnnotation, AllowICMPAnnotation} {
		if v, ok := desired.Annotations[k]; ok {
			if live.Annotations[k] != v {
				live.Annotations[k] = v
				changed = true
			}
		} else if _, ok := live.Annotations[k]; ok {
			delete(live.Annotations, k)
			changed = true
		}
	}
	if live.Spec.Type != desired.Spec.Type {
		live.Spec.Type = desired.Spec.Type
		changed = true
	}
	if live.Spec.ExternalTrafficPolicy != desired.Spec.ExternalTrafficPolicy {
		live.Spec.ExternalTrafficPolicy = desired.Spec.ExternalTrafficPolicy
		changed = true
	}
	if !equalStringMaps(live.Spec.Selector, desired.Spec.Selector) {
		live.Spec.Selector = copyMap(desired.Spec.Selector)
		changed = true
	}
	if !equalPorts(live.Spec.Ports, desired.Spec.Ports) {
		live.Spec.Ports = mergeNodePorts(live.Spec.Ports, desired.Spec.Ports)
		changed = true
	}
	if !equalBoolPtr(live.Spec.AllocateLoadBalancerNodePorts, desired.Spec.AllocateLoadBalancerNodePorts) {
		live.Spec.AllocateLoadBalancerNodePorts = desired.Spec.AllocateLoadBalancerNodePorts
		changed = true
	}
	return changed
}

// mergeNodePorts keeps the node ports the apiserver already allocated for
// ports that survive the update, so a mirror refresh does not reallocate them.
func mergeNodePorts(live, desired []corev1.ServicePort) []corev1.ServicePort {
	byPort := map[string]int32{}
	for _, p := range live {
		byPort[portKey(p)] = p.NodePort
	}
	out := copyPorts(desired)
	for i := range out {
		out[i].NodePort = byPort[portKey(out[i])]
	}
	return out
}

func portKey(p corev1.ServicePort) string {
	proto := p.Protocol
	if proto == "" {
		proto = corev1.ProtocolTCP
	}
	return fmt.Sprintf("%s/%d", proto, p.Port)
}

func equalPorts(a, b []corev1.ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		pa, pb := a[i], b[i]
		if pa.Name != pb.Name || pa.Port != pb.Port || pa.TargetPort != pb.TargetPort ||
			normProto(pa.Protocol) != normProto(pb.Protocol) || !equalStringPtr(pa.AppProtocol, pb.AppProtocol) {
			return false
		}
	}
	return true
}

func normProto(p corev1.Protocol) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return p
}

// ownedService finds the Service rendered for an attachment. Identity is the
// controller owner reference: a Service carrying the ownership label but
// controlled by anything else is not this attachment's and is never adopted.
func (r *Reconciler) ownedService(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*corev1.Service, error) {
	list := &corev1.ServiceList{}
	if err := r.List(ctx, list, client.InNamespace(ea.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if isControlledBy(&list.Items[i], ea) {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// backendPods returns the pods an endpoint Service currently routes to,
// counting only ready endpoints so that a terminating replica or a not-yet-
// ready surge pod does not count as a second backend.
func (r *Reconciler) backendPods(ctx context.Context, svc *corev1.Service) (map[types.UID]string, error) {
	slices := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, slices, client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return nil, err
	}
	pods := map[types.UID]string{}
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && !*e.Conditions.Ready {
				continue
			}
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" {
				continue
			}
			pods[e.TargetRef.UID] = e.TargetRef.Name
		}
	}
	return pods, nil
}

// delegatedRival finds another Service delegated to the datapath proxy that
// selects any of the same backend pods. The physical unit of exclusivity is
// the backend pod, and it covers both render modes: cozy-proxy's egress map
// is keyed by pod and holds one service IP, and a second delegated Service on
// the same pod evicts the first from both directions.
//
// The oldest delegation wins, ties broken by name, so that two reconcilers
// never fight over the slot. A rival is reported only when it would win
// against this attachment's own Service (or when that Service does not
// exist yet).
func (r *Reconciler) delegatedRival(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment, own *corev1.Service, backends map[types.UID]string) (*corev1.Service, error) {
	if len(backends) == 0 {
		return nil, nil
	}
	list := &corev1.ServiceList{}
	if err := r.List(ctx, list, client.InNamespace(ea.Namespace),
		client.MatchingLabels{ServiceProxyNameLabel: ServiceProxyName}); err != nil {
		return nil, err
	}
	var rivals []*corev1.Service
	for i := range list.Items {
		other := &list.Items[i]
		if own != nil && other.UID == own.UID {
			continue
		}
		theirs, err := r.backendPods(ctx, other)
		if err != nil {
			return nil, err
		}
		for uid := range theirs {
			if _, shared := backends[uid]; shared {
				rivals = append(rivals, other)
				break
			}
		}
	}
	if len(rivals) == 0 {
		return nil, nil
	}
	sort.Slice(rivals, func(i, j int) bool { return older(rivals[i], rivals[j]) })
	if own != nil && older(own, rivals[0]) {
		return nil, nil
	}
	return rivals[0], nil
}

func older(a, b *corev1.Service) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}
