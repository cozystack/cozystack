package fluxshardoperator

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func webhookWithNamespaces(t *testing.T, namespaces ...*corev1.Namespace) *ShardWebhook {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, ns := range namespaces {
		builder = builder.WithObjects(ns)
	}
	return &ShardWebhook{Reader: builder.Build()}
}

func createRequest(t *testing.T, namespace, name string, labels map[string]string) admission.Request {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels":    labels,
		},
		"spec": map[string]any{"interval": "5m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Namespace: namespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func nsWithShard(name, shard string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if shard != "" {
		ns.Labels = map[string]string{TenantShardLabel: shard}
	}
	return ns
}

func TestShardWebhookStampsAssignedShard(t *testing.T) {
	h := webhookWithNamespaces(t, nsWithShard("tenant-foo", "shard1"))
	resp := h.Handle(context.Background(),
		createRequest(t, "tenant-foo", "postgres-db1", map[string]string{ShardKeyLabel: LegacyShardKey}))
	if !resp.Allowed {
		t.Fatalf("response not allowed: %v", resp)
	}
	if len(resp.Patches) != 1 {
		t.Fatalf("expected exactly one patch, got %v", resp.Patches)
	}
	p := resp.Patches[0]
	if p.Operation != "add" || p.Path != "/metadata/labels/sharding.fluxcd.io~1key" || p.Value != "shard1" {
		t.Fatalf("unexpected patch: %+v", p)
	}
}

func TestShardWebhookParentTenantHR(t *testing.T) {
	// The parent HR of nested tenant "bar" under "tenant-foo" must be stamped
	// with tenant-foo-bar's shard, not tenant-foo's.
	h := webhookWithNamespaces(t,
		nsWithShard("tenant-foo", "shard0"),
		nsWithShard("tenant-foo-bar", "shard2"),
	)
	resp := h.Handle(context.Background(), createRequest(t, "tenant-foo", "tenant-bar", map[string]string{
		ShardKeyLabel:        LegacyShardKey,
		ApplicationKindLabel: TenantKind,
	}))
	if len(resp.Patches) != 1 || resp.Patches[0].Value != "shard2" {
		t.Fatalf("expected stamp of the nested tenant's shard2: %+v", resp.Patches)
	}
}

func TestShardWebhookMisses(t *testing.T) {
	h := webhookWithNamespaces(t,
		nsWithShard("tenant-unassigned", ""),
		nsWithShard("tenant-bogus", "tenants"),
	)
	cases := []struct {
		desc string
		req  admission.Request
	}{
		{"namespace not found", createRequest(t, "tenant-ghost", "app", map[string]string{ShardKeyLabel: LegacyShardKey})},
		{"no assignment recorded", createRequest(t, "tenant-unassigned", "app", map[string]string{ShardKeyLabel: LegacyShardKey})},
		{"non-canonical assignment ignored", createRequest(t, "tenant-bogus", "app", map[string]string{ShardKeyLabel: LegacyShardKey})},
		{"non-tenant namespace", createRequest(t, "cozy-system", "app", map[string]string{ShardKeyLabel: LegacyShardKey})},
	}
	for _, c := range cases {
		resp := h.Handle(context.Background(), c.req)
		if !resp.Allowed || len(resp.Patches) != 0 {
			t.Errorf("%s: expected permissive no-op, got allowed=%v patches=%v", c.desc, resp.Allowed, resp.Patches)
		}
	}
}

func TestShardWebhookIgnoresNonCreate(t *testing.T) {
	h := webhookWithNamespaces(t, nsWithShard("tenant-foo", "shard1"))
	req := createRequest(t, "tenant-foo", "app", map[string]string{ShardKeyLabel: LegacyShardKey})
	req.Operation = admissionv1.Update
	resp := h.Handle(context.Background(), req)
	if !resp.Allowed || len(resp.Patches) != 0 {
		t.Fatalf("UPDATE must never be mutated: %v", resp)
	}
}

func TestShardWebhookAlreadyCorrect(t *testing.T) {
	h := webhookWithNamespaces(t, nsWithShard("tenant-foo", "shard1"))
	resp := h.Handle(context.Background(),
		createRequest(t, "tenant-foo", "app", map[string]string{ShardKeyLabel: "shard1"}))
	if !resp.Allowed || len(resp.Patches) != 0 {
		t.Fatalf("correctly labeled HR must pass untouched: %v", resp)
	}
}
