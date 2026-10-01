// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// gatewayVMIOnNode builds the VMI the way KubeVirt reports it: activePods maps pod UID
// to the node that pod is on, nodeName is the node the guest is running on. Pass
// two activePods entries to stand in for a live migration in flight.
func gatewayVMIOnNode(instance, nodeName string, activePods map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(vmiGVK)
	u.SetNamespace("tenant-test")
	u.SetName(releasePrefix + instance)
	active := map[string]interface{}{}
	for uid, node := range activePods {
		active[uid] = node
	}
	_ = unstructured.SetNestedMap(u.Object, active, "status", "activePods")
	_ = unstructured.SetNestedField(u.Object, nodeName, "status", "nodeName")
	return u
}

func gwPodWithUID(name, instance, podIP, uid, node string) *corev1.Pod {
	p := gwPod(name, instance, podIP)
	p.UID = types.UID(uid)
	p.Spec.NodeName = node
	return p
}

// TestDiscoverGatewayPod_PrefersTheVMIsOwnActivePod covers the case list order
// cannot answer: two Running virt-launcher pods carrying the same VM name, which
// is what a live migration produces. Names carry a random suffix, so whichever
// the List returns first is arbitrary between source and target, and the caller
// installs the chosen pod's IP as the tenant's next hop and pushes the router
// config to it.
func TestDiscoverGatewayPod_PrefersTheVMIsOwnActivePod(t *testing.T) {
	const (
		sourceUID = "11111111-1111-1111-1111-111111111111"
		targetUID = "22222222-2222-2222-2222-222222222222"
	)
	// Deliberately ordered so the first Running pod is the WRONG one: without the
	// VMI read the old selection returns virt-launcher-aaa.
	source := gwPodWithUID("virt-launcher-aaa", "demo", "10.244.0.5", sourceUID, "node-a")
	target := gwPodWithUID("virt-launcher-zzz", "demo", "10.244.0.9", targetUID, "node-b")
	vmi := gatewayVMIOnNode("demo", "node-b", map[string]string{sourceUID: "node-a", targetUID: "node-b"})

	r := newTestReconciler(t, source, target, vmi)
	got, err := r.discoverGatewayPod(context.Background(), &instance{name: "demo", namespace: "tenant-test"})
	if err != nil {
		t.Fatalf("discoverGatewayPod: %v", err)
	}
	if got == nil || got.Name != "virt-launcher-zzz" {
		t.Fatalf("discoverGatewayPod picked %v, want virt-launcher-zzz (the pod on the VMI's nodeName)", got)
	}
}

// TestDiscoverGatewayPod_FallsBackWhenTheVMIIsUnreadable keeps the behaviour
// every non-migrating instance already had, including during first boot when the
// VMI has not appeared yet.
func TestDiscoverGatewayPod_FallsBackWhenTheVMIIsUnreadable(t *testing.T) {
	only := gwPodWithUID("virt-launcher-aaa", "demo", "10.244.0.5", "33333333-3333-3333-3333-333333333333", "node-a")

	r := newTestReconciler(t, only)
	got, err := r.discoverGatewayPod(context.Background(), &instance{name: "demo", namespace: "tenant-test"})
	if err != nil {
		t.Fatalf("discoverGatewayPod: %v", err)
	}
	if got == nil || got.Name != "virt-launcher-aaa" {
		t.Fatalf("discoverGatewayPod picked %v, want virt-launcher-aaa", got)
	}
}

// TestDefaultVyOSClientFactory_RefusesWithoutAPinnedCA is the negative half of
// the management-channel posture: the factory has no unverified fallback, so an
// endpoint whose api-key Secret carries no certificate fails the push instead of
// dialling a connection nobody verifies. The token does not rescue it, since
// whoever can intercept the connection collects the token with it.
func TestDefaultVyOSClientFactory_RefusesWithoutAPinnedCA(t *testing.T) {
	for _, tc := range []struct {
		name string
		ep   VyOSEndpoint
	}{
		{"no CA at all", VyOSEndpoint{URL: "https://10.244.0.5", Token: "t", ServerName: "gw"}},
		{"CA but no server name", VyOSEndpoint{URL: "https://10.244.0.5", Token: "t", CAPEM: []byte("-----BEGIN CERTIFICATE-----")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vc, err := DefaultVyOSClientFactory(tc.ep)
			if err == nil {
				t.Fatalf("DefaultVyOSClientFactory(%s) = %v, nil; want an error", tc.name, vc)
			}
			if vc != nil {
				t.Fatalf("DefaultVyOSClientFactory(%s) returned a client alongside its error", tc.name)
			}
		})
	}
}

// TestDefaultVyOSClientFactory_BuildsAPinnedClient is the positive half, so the
// refusal above cannot pass by refusing everything.
func TestDefaultVyOSClientFactory_BuildsAPinnedClient(t *testing.T) {
	vc, err := DefaultVyOSClientFactory(VyOSEndpoint{
		URL:        "https://10.244.0.5",
		Token:      "t",
		CAPEM:      []byte("-----BEGIN CERTIFICATE-----"),
		ServerName: "gw",
	})
	if err != nil {
		t.Fatalf("DefaultVyOSClientFactory with a pinned CA: %v", err)
	}
	if vc == nil {
		t.Fatal("DefaultVyOSClientFactory returned no client and no error")
	}
}
