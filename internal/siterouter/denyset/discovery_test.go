// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package denyset

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDiscoverClusterNetworksIncludesLiveNodeAndServiceAddresses(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "cozy-system"},
		Data: map[string]string{
			ConfigMapKeyPodCIDR:     "10.244.0.0/16",
			ConfigMapKeyServiceCIDR: "10.96.0.0/16",
			ConfigMapKeyJoinCIDR:    "100.64.0.0/16",
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "192.168.100.10"},
			{Type: corev1.NodeExternalIP, Address: "203.0.113.10"},
			{Type: corev1.NodeHostName, Address: "worker-0"},
		}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "tenant-test"},
		Spec: corev1.ServiceSpec{
			Type:           corev1.ServiceTypeLoadBalancer,
			LoadBalancerIP: "198.51.100.20",
			ExternalIPs:    []string{"198.51.100.21"},
		},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "198.51.100.22"}, {Hostname: "lb.example.test"}},
		}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm, node, svc).Build()

	got, err := DiscoverClusterNetworks(context.Background(), reader, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack"})
	if err != nil {
		t.Fatalf("DiscoverClusterNetworks: %v", err)
	}
	if want := []string{"192.168.100.10/32", "203.0.113.10/32"}; !equalStrings(got.NodeCIDRs, want) {
		t.Errorf("NodeCIDRs = %v, want %v", got.NodeCIDRs, want)
	}
	if want := []string{"198.51.100.20/32", "198.51.100.21/32", "198.51.100.22/32"}; !equalStrings(got.LBPools, want) {
		t.Errorf("LBPools = %v, want %v", got.LBPools, want)
	}

	rejections := Validate([]string{"192.168.100.0/24", "198.51.100.0/24"}, got)
	if len(rejections) != 2 || rejections[0].Network != NetworkNode || rejections[1].Network != NetworkLBPool {
		t.Fatalf("live node/LB addresses must reject containing subnets, got %+v", rejections)
	}
}

func TestDiscoverClusterNetworksMissingConfigMapStillUsesDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()

	got, err := DiscoverClusterNetworks(context.Background(), reader, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack"})
	if err != nil {
		t.Fatalf("DiscoverClusterNetworks: %v", err)
	}
	if got.PodCIDR != DefaultPodCIDR || got.ServiceCIDR != DefaultServiceCIDR || got.JoinCIDR != DefaultJoinCIDR {
		t.Fatalf("missing ConfigMap must use platform defaults, got %+v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
