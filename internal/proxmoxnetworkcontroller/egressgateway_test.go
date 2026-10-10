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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// gatewayChain builds a VpcEgressGateway rendered by the vpc chart, the
// Deployment Kube-OVN creates for it (plain owner reference, no controller
// flag, as util.SetOwnerReference does), its ReplicaSet and one pod, with port
// security turned on the way the cozystack kube-ovn webhook leaves it.
func gatewayChain(vegLabels map[string]string, vpc string) []client.Object {
	veg := &VpcEgressGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc-x-egress", Namespace: tenantA, UID: types.UID("veg-uid"), Labels: vegLabels},
		Spec:       VpcEgressGatewaySpec{VPC: vpc},
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "vpc-x-egress", Namespace: tenantA, UID: types.UID("dep-uid"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "kubeovn.io/v1", Kind: "VpcEgressGateway", Name: "vpc-x-egress", UID: "veg-uid"}},
	}}
	yes := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "vpc-x-egress-abc", Namespace: tenantA, UID: types.UID("rs-uid"),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "vpc-x-egress", UID: "dep-uid", Controller: &yes}},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "vpc-x-egress-abc-1", Namespace: tenantA,
		Labels:          map[string]string{vegLabel: "vpc-x-egress"},
		Annotations:     map[string]string{portSecurityAnnotation: "true"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "vpc-x-egress-abc", UID: "rs-uid", Controller: &yes}},
	}}
	return []client.Object{veg, dep, rs, pod}
}

func reconcileGatewayPod(t *testing.T, objs ...client.Object) *corev1.Pod {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	r := &EgressGatewayPodReconciler{Client: c, Reader: c}
	key := client.ObjectKey{Namespace: tenantA, Name: "vpc-x-egress-abc-1"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{}
	if err := c.Get(context.Background(), key, pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func TestEgressGatewayPodGetsPortSecurityOff(t *testing.T) {
	pod := reconcileGatewayPod(t, gatewayChain(map[string]string{vpcIDLabel: "vpc-x"}, "vpc-x")...)
	if got := pod.Annotations[portSecurityAnnotation]; got != "false" {
		t.Fatalf("port_security = %q, want false on a gateway the vpc chart rendered", got)
	}
}

func TestPodsOutsideAChartGatewayKeepPortSecurity(t *testing.T) {
	// A gateway without the vpc chart's label: not ours.
	pod := reconcileGatewayPod(t, gatewayChain(nil, "vpc-x")...)
	if got := pod.Annotations[portSecurityAnnotation]; got != "true" {
		t.Fatalf("port_security = %q on a gateway the vpc chart did not render", got)
	}

	// A label pointing at another VPC than the gateway's own.
	pod = reconcileGatewayPod(t, gatewayChain(map[string]string{vpcIDLabel: "vpc-other"}, "vpc-x")...)
	if got := pod.Annotations[portSecurityAnnotation]; got != "true" {
		t.Fatalf("port_security = %q on a gateway whose label names another VPC", got)
	}

	// The gateway label on a pod with no ReplicaSet behind it, such as a
	// virt-launcher pod whose VM template carries the label.
	objs := gatewayChain(map[string]string{vpcIDLabel: "vpc-x"}, "vpc-x")
	pod0 := objs[3].(*corev1.Pod)
	pod0.OwnerReferences = nil
	pod = reconcileGatewayPod(t, objs...)
	if got := pod.Annotations[portSecurityAnnotation]; got != "true" {
		t.Fatalf("port_security = %q on a labelled pod that no gateway owns", got)
	}

	// A Deployment that is not owned by the gateway the pod names.
	objs = gatewayChain(map[string]string{vpcIDLabel: "vpc-x"}, "vpc-x")
	objs[1].(*appsv1.Deployment).OwnerReferences = nil
	pod = reconcileGatewayPod(t, objs...)
	if got := pod.Annotations[portSecurityAnnotation]; got != "true" {
		t.Fatalf("port_security = %q on a Deployment no gateway owns", got)
	}
}
