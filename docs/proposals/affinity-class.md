# AffinityClass: Named Placement Classes for CozyStack Applications (Draft)

## Concept

Similar to StorageClass in Kubernetes, a new resource **AffinityClass** is introduced — a named abstraction over scheduling constraints. When creating an Application, the user selects an AffinityClass by name without knowing the details of the cluster topology.

```
StorageClass     →  "which disk"       →  PV provisioning
AffinityClass    →  "where to place"   →  Pod scheduling
```

## Design

### 1. AffinityClass CRD

A cluster-scoped resource created by the platform administrator:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: AffinityClass
metadata:
  name: dc1
spec:
  # nodeSelector that MUST be present on every pod of the application.
  # Used for validation by the lineage webhook.
  nodeSelector:
    topology.kubernetes.io/zone: dc1
```

```yaml
apiVersion: cozystack.io/v1alpha1
kind: AffinityClass
metadata:
  name: dc2
spec:
  nodeSelector:
    topology.kubernetes.io/zone: dc2
```

```yaml
apiVersion: cozystack.io/v1alpha1
kind: AffinityClass
metadata:
  name: gpu
spec:
  nodeSelector:
    node.kubernetes.io/gpu: "true"
```

An AffinityClass contains a `nodeSelector` — a set of key=value pairs that must be present in `pod.spec.nodeSelector` on every pod of the application. This is a contract: the chart is responsible for setting these selectors, the webhook is responsible for verifying them.

### 2. Tenant: Restricting Available Classes

Tenant gets `allowedAffinityClasses` and `defaultAffinityClass` fields:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: acme
  namespace: tenant-root
spec:
  defaultAffinityClass: dc1          # default class for applications
  allowedAffinityClasses:            # which classes are allowed
    - dc1
    - dc2
  etcd: false
  ingress: true
  monitoring: false
```

These values are propagated to the `cozystack-values` Secret in the child namespace:

```yaml
# Secret cozystack-values in namespace tenant-acme
stringData:
  values.yaml: |
    _cluster:
      # ... existing cluster config
    _namespace:
      # ... existing namespace config
      defaultAffinityClass: dc1
      allowedAffinityClasses:
        - dc1
        - dc2
```

### 3. Application: Selecting a Class

Each application can specify an `affinityClass`. If not specified, the `defaultAffinityClass` from the tenant is used:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: main-db
  namespace: tenant-acme
spec:
  affinityClass: dc1       # explicit selection
  replicas: 3
```

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: cache
  namespace: tenant-acme
spec:
  # affinityClass not specified → uses tenant's defaultAffinityClass (dc1)
  replicas: 2
```

### 4. How affinityClass Reaches the HelmRelease

When creating an Application, the API server (`pkg/registry/apps/application/rest.go`):

1. Extracts `affinityClass` from `spec` (or uses the default from `cozystack-values`)
2. Records `affinityClass` as a **label on the HelmRelease**:
   ```
   apps.cozystack.io/affinity-class: dc1
   ```
3. Resolves AffinityClass to `nodeSelector` and passes it into HelmRelease values as `_scheduling`:
   ```yaml
   _scheduling:
     affinityClass: dc1
     nodeSelector:
       topology.kubernetes.io/zone: dc1
   ```

### 5. How Charts Apply Scheduling

A helper is added to `cozy-lib`:

```yaml
{{- define "cozy-lib.scheduling.nodeSelector" -}}
{{- if .Values._scheduling }}
{{- if .Values._scheduling.nodeSelector }}
nodeSelector:
  {{- .Values._scheduling.nodeSelector | toYaml | nindent 2 }}
{{- end }}
{{- end }}
{{- end -}}
```

Each app chart uses the helper when rendering Pod/StatefulSet/Deployment specs:

```yaml
# packages/apps/postgres/templates/db.yaml
spec:
  instances: {{ .Values.replicas }}
  {{- include "cozy-lib.scheduling.nodeSelector" . | nindent 2 }}
```

```yaml
# packages/apps/redis/templates/redis.yaml
spec:
  replicas: {{ .Values.replicas }}
  template:
    spec:
      {{- include "cozy-lib.scheduling.nodeSelector" . | nindent 6 }}
```

Charts **must** apply `_scheduling.nodeSelector`. If they don't, pods will be rejected by the webhook.

---

## Validation via Lineage Webhook

### Why Validation, Not Mutation

Mutation (injecting nodeSelector into a pod) creates problems:
- Requires merging with existing pod nodeSelector/affinity — complex logic with edge cases
- Operators (CNPG, Strimzi) may overwrite nodeSelector on pod restart
- Hidden behavior: pod is created with one spec but actually runs with another

Validation is simpler and more reliable:
- Webhook checks: "does this pod **have** the required nodeSelector?"
- If not, the pod is **rejected** with a clear error message
- The chart and operator are responsible for setting the correct spec

### What Already Exists in the Lineage Webhook

The lineage webhook (`internal/lineagecontrollerwebhook/webhook.go`) on every Pod creation:

1. Decodes the Pod
2. Walks the ownership graph (`lineage.WalkOwnershipGraph`) — finds the **owning HelmRelease**
3. Extracts labels from the HelmRelease: `apps.cozystack.io/application.kind`, `.group`, `.name`
4. Applies these labels to the Pod

**Key point:** the webhook already knows which HelmRelease owns each Pod.

### What Is Added

After computing lineage labels, a validation step is added:

```
Handle(pod):
  1. [existing] computeLabels(pod)          → finds owning HelmRelease
  2. [existing] applyLabels(pod, labels)     → mutates labels
  3. [NEW]      validateAffinity(pod, hr)    → checks nodeSelector
  4. Return patch or Denied
```

The `validateAffinity` logic:

```go
func (h *LineageControllerWebhook) validateAffinity(
    ctx context.Context,
    pod *unstructured.Unstructured,
    hr *helmv2.HelmRelease,
) *admission.Response {
    // 1. Extract affinityClass from HelmRelease label
    affinityClassName, ok := hr.Labels["apps.cozystack.io/affinity-class"]
    if !ok {
        return nil // no affinityClass — no validation needed
    }

    // 2. Look up AffinityClass from cache
    affinityClass, ok := h.affinityClassMap[affinityClassName]
    if !ok {
        resp := admission.Denied(fmt.Sprintf(
            "AffinityClass %q not found", affinityClassName))
        return &resp
    }

    // 3. Check pod's nodeSelector
    podNodeSelector := extractNodeSelector(pod) // from pod.spec.nodeSelector
    for key, expected := range affinityClass.Spec.NodeSelector {
        actual, exists := podNodeSelector[key]
        if !exists || actual != expected {
            resp := admission.Denied(fmt.Sprintf(
                "pod %s/%s belongs to application with AffinityClass %q "+
                "but missing required nodeSelector %s=%s",
                pod.GetNamespace(), pod.GetName(),
                affinityClassName, key, expected))
            return &resp
        }
    }

    return nil // validation passed
}
```

### AffinityClass Caching

The lineage webhook controller already caches ApplicationDefinitions (`runtimeConfig.appCRDMap`). An AffinityClass cache is added in the same way:

```go
type runtimeConfig struct {
    appCRDMap        map[appRef]*cozyv1alpha1.ApplicationDefinition
    affinityClassMap map[string]*cozyv1alpha1.AffinityClass  // NEW
}
```

The controller adds a watch on AffinityClass:

```go
func (c *LineageControllerWebhook) SetupWithManagerAsController(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&cozyv1alpha1.ApplicationDefinition{}).
        Watches(&cozyv1alpha1.AffinityClass{}, &handler.EnqueueRequestForObject{}).
        Complete(c)
}
```

When an AffinityClass changes, the cache is rebuilt.

---

## End-to-End Flow

```
1. Admin creates AffinityClass "dc1" (nodeSelector: zone=dc1)

2. Admin creates Tenant "acme" (defaultAffinityClass: dc1, allowed: [dc1, dc2])
   → namespace tenant-acme
   → cozystack-values Secret with defaultAffinityClass

3. User creates Postgres "main-db" (affinityClass: dc1)
   → API server checks: dc1 ∈ allowedAffinityClasses? ✓
   → API server resolves AffinityClass → nodeSelector
   → HelmRelease is created with:
      - label: apps.cozystack.io/affinity-class=dc1
      - values: _scheduling.nodeSelector.topology.kubernetes.io/zone=dc1

4. FluxCD deploys HelmRelease → Helm renders the chart
   → Chart uses cozy-lib helper
   → CNPG Cluster is created with nodeSelector: {zone: dc1}

5. CNPG operator creates Pod
   → Pod has nodeSelector: {zone: dc1}

6. Lineage webhook intercepts the Pod:
   a. WalkOwnershipGraph → finds HelmRelease "main-db"
   b. HelmRelease label → affinityClass=dc1
   c. AffinityClass "dc1" → nodeSelector: {zone: dc1}
   d. Checks: pod.spec.nodeSelector contains zone=dc1? ✓
   e. Admits Pod (+ standard lineage labels)

7. Scheduler places the Pod on a node in dc1
```

### Error Scenario (chart forgot to apply nodeSelector):

```
5. CNPG operator creates Pod WITHOUT nodeSelector

6. Lineage webhook:
   d. Checks: pod.spec.nodeSelector contains zone=dc1? ✗
   e. REJECTS Pod:
      "pod main-db-1 belongs to application with AffinityClass dc1
       but missing required nodeSelector topology.kubernetes.io/zone=dc1"

7. Pod is not created. CNPG operator sees the error and retries.
   → Chart developer gets a signal that the chart does not support scheduling.
```

---

## Code Changes

### New Files

| File                                                 | Description             |
|------------------------------------------------------|-------------------------|
| `api/v1alpha1/affinityclass_types.go`                | AffinityClass CRD types |
| `config/crd/bases/cozystack.io_affinityclasses.yaml` | CRD manifest            |

### Modified Files

| File                                                  | Change                                                            |
|-------------------------------------------------------|-------------------------------------------------------------------|
| `internal/lineagecontrollerwebhook/webhook.go`        | Add `validateAffinity()` to `Handle()`                            |
| `internal/lineagecontrollerwebhook/config.go`         | Add `affinityClassMap` to `runtimeConfig`                         |
| `internal/lineagecontrollerwebhook/controller.go`     | Add watch on AffinityClass                                        |
| `pkg/registry/apps/application/rest.go`               | On Create/Update: resolve affinityClass, pass to values and label |
| `packages/apps/tenant/values.yaml`                    | Add `defaultAffinityClass`, `allowedAffinityClasses`              |
| `packages/apps/tenant/templates/namespace.yaml`       | Propagate to cozystack-values                                     |
| `packages/system/tenant-rd/cozyrds/tenant.yaml`       | Extend OpenAPI schema                                             |
| `packages/library/cozy-lib/templates/_cozyconfig.tpl` | Add `cozy-lib.scheduling.nodeSelector` helper                     |
| `packages/apps/*/templates/*.yaml`                    | Each app chart: add helper usage                                  |

---

## Open Questions

1. **AffinityClass outside Tenants**: Should AffinityClass work for applications outside tenant namespaces (system namespace)? Or only for tenant workloads?

2. **affinityClass validation on Application creation**: The API server should verify that the specified affinityClass exists and is included in the tenant's `allowedAffinityClasses`. Where should this be done — in the REST handler (`rest.go`) or in a separate validating webhook?

3. **Soft mode (warn vs deny)**: Is a mode needed where the webhook issues a warning instead of rejecting? This would simplify gradual adoption while not all charts support `_scheduling`.

4. **affinityClass inheritance**: If a child Tenant does not specify `defaultAffinityClass`, should it be inherited from the parent? The current `cozystack-values` architecture supports this inheritance natively.

5. **Multiple nodeSelectors**: Is OR-logic support needed (pod can be in dc1 OR dc2)? With `nodeSelector` this is impossible — AffinityClass would need to be extended to `nodeAffinity`. However, validation becomes significantly more complex.
