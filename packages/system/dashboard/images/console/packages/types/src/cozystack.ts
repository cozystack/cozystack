import type { K8sCondition, K8sResource } from "@cozystack/k8s-client"

export const COZYSTACK_GROUP = "cozystack.io"
export const COZYSTACK_VERSION = "v1alpha1"

export const APPS_GROUP = "apps.cozystack.io"
export const APPS_VERSION = "v1alpha1"

/**
 * ApplicationDefinition describes a single application kind available in the
 * cluster (postgres, kafka, kubernetes, tenant, etc.). The UI reads all ADs to
 * build the marketplace dynamically.
 */
export interface ApplicationDefinition extends K8sResource<ApplicationDefinitionSpec> {
  kind: "ApplicationDefinition"
}

export interface ApplicationDefinitionSpec {
  application: {
    kind: string
    plural: string
    singular: string
    /** JSON-encoded OpenAPI schema for the application values. */
    openAPISchema: string
  }
  dashboard?: {
    category?: string
    description?: string
    /** Base64-encoded SVG icon. */
    icon?: string
    /** Ordered list of key paths used to render the form top-to-bottom. */
    keysOrder?: string[][]
    /**
     * Tenant module: singleton add-on managed via `Tenant.spec.<name>=true`.
     * UI must hide these from the regular marketplace and category nav, and
     * surface them under Administration → Modules.
     */
    module?: boolean
  }
  release?: {
    prefix?: string
    chartRef?: {
      kind: string
      name: string
      namespace: string
    }
    labels?: Record<string, string>
  }
  secrets?: {
    include?: string[]
    exclude?: string[]
  }
}

/**
 * Generic Cozystack application instance — Postgres, Kafka, Kubernetes, VPN,
 * etc. All of them share the same shape: a free-form `spec` driven by the
 * application's OpenAPI schema, plus a conditions-based `status`.
 */
export interface ApplicationInstance
  extends K8sResource<Record<string, unknown>, ApplicationInstanceStatus> {}

export interface ApplicationInstanceStatus {
  conditions?: K8sCondition[]
  version?: string
  [k: string]: unknown
}

/**
 * Tenant is a special instance of the `tenants` application that also creates
 * a child namespace named `tenant-<name>` for hosting further applications.
 */
export interface Tenant extends ApplicationInstance {
  kind: "Tenant"
  spec?: {
    host?: string
    etcd?: boolean
    ingress?: boolean
    monitoring?: boolean
    seaweedfs?: boolean
    isolated?: boolean
    resourceQuotas?: Record<string, string>
    [k: string]: unknown
  }
  status?: ApplicationInstanceStatus & {
    namespace?: string
  }
}

/**
 * TenantNamespace (`core.cozystack.io/v1alpha1`) is a cluster-scoped
 * aggregation that tracks every namespace owned by a tenant. Its name is the
 * namespace itself (e.g. `tenant-kvaps`) and the labels encode the tenant
 * hierarchy and which parent namespace each tenant module is installed into.
 */
export interface TenantNamespace extends K8sResource {
  kind: "TenantNamespace"
}
