package fluxshardoperator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WebhookPath is the HTTP path the shard-stamping webhook is served on.
const WebhookPath = "/mutate-helmrelease-shard"

// ShardWebhook stamps the owning tenant's shard onto every tenant HelmRelease
// at CREATE time, so each HelmRelease is born on the correct shard regardless
// of creation path (API-created, tenant chart children, "extra" HelmReleases
// rendered inside other releases).
//
// It is registered with operations: ["CREATE"] and failurePolicy: Ignore only:
// helm-controller status patches are a firehose, so UPDATE is never
// intercepted, and a webhook outage must degrade to the catch-all path (the
// HelmRelease keeps its legacy "tenants" key until the placement controller
// relabels it) instead of blocking creation.
//
// UPDATE does not need interception: the only full-object writer of tenant
// HelmReleases is the cozystack-api Update handler, which carries the live
// shard label over (pkg/registry/apps/application/rest.go); helm-controller
// itself applies child HelmReleases with server-side apply, which leaves
// labels owned by other field managers untouched.
type ShardWebhook struct {
	// Reader resolves tenant namespaces; backed by the manager's metadata
	// cache, so lookups are in-memory.
	Reader client.Reader
}

// SetupWithManager registers the admission handler on the webhook server of
// every replica (webhooks are not leader-gated).
func (h *ShardWebhook) SetupWithManager(mgr ctrl.Manager) error {
	mgr.GetWebhookServer().Register(WebhookPath, &admission.Webhook{Handler: h})
	return nil
}

// Handle stamps the recorded shard assignment onto a newly created
// HelmRelease. Every miss is permissive: correctness is restored by the
// placement controller, the webhook only removes the handoff gap.
func (h *ShardWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("only CREATE is mutated")
	}

	obj := &metav1.PartialObjectMetadata{}
	if err := json.Unmarshal(req.Object.Raw, obj); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding object metadata: %w", err))
	}
	// Admission objects may omit metadata.namespace before defaulting.
	if obj.Namespace == "" {
		obj.Namespace = req.Namespace
	}

	tenantNS, ok := TenantNamespaceForHR(obj)
	if !ok {
		return admission.Allowed("not a tenant HelmRelease")
	}

	ns := NamespaceMeta()
	if err := h.Reader.Get(ctx, types.NamespacedName{Name: tenantNS}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			// Rare: the tenant namespace is not created yet (e.g. the parent
			// tenant HelmRelease arrives before its chart renders the
			// namespace). The placement controller assigns on first sight.
			return admission.Allowed("tenant namespace not found, deferring to the placement controller")
		}
		log.FromContext(ctx).Error(err, "resolving tenant namespace", "tenant", tenantNS)
		return admission.Allowed("tenant namespace lookup failed, deferring to the placement controller")
	}

	shard := ns.Labels[TenantShardLabel]
	if _, ok := ParseShardIndex(shard); !ok {
		return admission.Allowed("tenant has no recorded shard assignment yet")
	}

	if obj.Labels[ShardKeyLabel] == shard {
		return admission.Allowed("already on the assigned shard")
	}

	// JSON Patch "add" both creates and overwrites object members.
	var op jsonpatch.JsonPatchOperation
	if obj.Labels == nil {
		op = jsonpatch.NewOperation("add", "/metadata/labels", map[string]string{ShardKeyLabel: shard})
	} else {
		op = jsonpatch.NewOperation("add", "/metadata/labels/"+escapeJSONPointer(ShardKeyLabel), shard)
	}
	return admission.Patched("stamped shard "+shard+" for tenant "+tenantNS, op)
}

// escapeJSONPointer escapes a JSON pointer path segment (RFC 6901).
func escapeJSONPointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}
