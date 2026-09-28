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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

// The address substrate (community #35) is consumed through one annotation
// and two Service fields, and read as unstructured so that this controller
// carries no module or CRD dependency on it. Everything below the annotation
// belongs to the substrate's per-class drivers.
const (
	// ClaimAnnotation names, on a LoadBalancer Service, the IPAddressClaim in
	// the Service's own namespace whose address the Service consumes.
	ClaimAnnotation = "local.sdn.cozystack.io/ip-address-claim"

	claimPhaseBound = "Bound"
)

var (
	substrateGroupVersion = schema.GroupVersion{Group: "local.sdn.cozystack.io", Version: "v1alpha1"}
	claimGVK              = substrateGroupVersion.WithKind("IPAddressClaim")
	claimListGVK          = substrateGroupVersion.WithKind("IPAddressClaimList")
	classGVK              = substrateGroupVersion.WithKind("IPAddressClass")
	addressGVK            = substrateGroupVersion.WithKind("IPAddress")
)

// claimView is the slice of an IPAddressClaim this controller reads.
type claimView struct {
	Name          string
	Family        string
	ClassName     string // spec.className, or status.className once the default class is resolved
	Bound         bool
	Addresses     []boundAddress
	WaitingReason string // why the claim is not bound, as the substrate reports it
}

type boundAddress struct {
	Name    string // the cluster-scoped IPAddress object
	Address string
}

func newClaim() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(claimGVK)
	return u
}

func parseClaim(u *unstructured.Unstructured) claimView {
	v := claimView{Name: u.GetName()}
	v.Family, _, _ = unstructured.NestedString(u.Object, "spec", "family")
	v.ClassName, _, _ = unstructured.NestedString(u.Object, "spec", "className")
	if v.ClassName == "" {
		v.ClassName, _, _ = unstructured.NestedString(u.Object, "status", "className")
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	v.Bound = phase == claimPhaseBound
	addrs, _, _ := unstructured.NestedSlice(u.Object, "status", "addresses")
	for _, a := range addrs {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		address, _ := m["address"].(string)
		if address != "" {
			v.Addresses = append(v.Addresses, boundAddress{Name: name, Address: address})
		}
	}
	if !v.Bound {
		v.WaitingReason = phase
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if status, _ := m["status"].(string); status == string(metav1.ConditionTrue) {
				continue
			}
			if reason, _ := m["reason"].(string); reason != "" {
				v.WaitingReason = reason
			}
			if msg, _ := m["message"].(string); msg != "" {
				v.WaitingReason += ": " + msg
			}
			break
		}
	}
	return v
}

// mintedClaim finds the claim this attachment owns, by controller owner
// reference. The claim is minted with generateName, so nothing but the owner
// reference identifies it.
func (r *Reconciler) mintedClaim(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(claimListGVK)
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

func (r *Reconciler) mintClaim(ctx context.Context, ea *cozyv1alpha1.EndpointAttachment) (*unstructured.Unstructured, error) {
	lb := ea.Spec.LoadBalancer
	u := newClaim()
	u.SetNamespace(ea.Namespace)
	u.SetGenerateName(ea.Name + "-")
	u.SetLabels(map[string]string{OwnerLabel: ea.Name})
	spec := map[string]interface{}{}
	if lb.ClassName != "" {
		spec["className"] = lb.ClassName
	}
	if lb.Family != "" {
		spec["family"] = string(lb.Family)
	}
	u.Object["spec"] = spec
	u.SetOwnerReferences([]metav1.OwnerReference{controllerOwnerReference(ea)})
	if err := r.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("mint IPAddressClaim: %w", err)
	}
	return u, nil
}

// classLoadBalancerClass returns the loadBalancerClass an IPAddressClass fixes
// for its Services. An empty value means the cluster default and is left
// empty on the Service.
func (r *Reconciler) classLoadBalancerClass(ctx context.Context, className string) (string, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(classGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: className}, u); err != nil {
		return "", err
	}
	lbClass, _, _ := unstructured.NestedString(u.Object, "spec", "loadBalancerClass")
	return lbClass, nil
}

// addressHolder returns the Service an IPAddress is currently announced on,
// or nil when the address is reserved but inert. The substrate records this
// as status.associatedTo on the cluster-scoped IPAddress.
func (r *Reconciler) addressHolder(ctx context.Context, addressName string) (*types.NamespacedName, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(addressGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: addressName}, u); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	assoc, found, _ := unstructured.NestedMap(u.Object, "status", "associatedTo")
	if !found {
		return nil, nil
	}
	kind, _ := assoc["kind"].(string)
	if kind != "" && kind != "Service" {
		return nil, nil
	}
	ns, _ := assoc["namespace"].(string)
	name, _ := assoc["name"].(string)
	if name == "" {
		return nil, nil
	}
	return &types.NamespacedName{Namespace: ns, Name: name}, nil
}

// substrateInstalled reports whether the claim kind is served, so that the
// reconciler can tell "waiting for the substrate" from an API error.
func substrateInstalled(err error) bool {
	return !apierrors.IsNotFound(err) && !isNoKindMatch(err)
}
