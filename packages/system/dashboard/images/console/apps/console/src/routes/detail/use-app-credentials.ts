import { K8sApiError, useK8sList, type K8sClient, type K8sResource } from "@cozystack/k8s-client"
import { APPS_GROUP, type ApplicationDefinition, type ApplicationInstance } from "@cozystack/types"

export const CREDENTIALS_GROUP = "core.cozystack.io"
export const CREDENTIALS_VERSION = "v1alpha1"

export type CredentialOrigin = "NotIssued" | "Migrated" | "Minted" | "Revoked"
export type CredentialOperation = "mint" | "revoke"

export interface CredentialHistoryEntry {
  operation: string
  at: string
  by: string
}

export type Credential = K8sResource<
  {
    application: { apiGroup: string; kind: string; name: string }
    user: string
  },
  {
    password?: { origin?: CredentialOrigin; at?: string; by?: string }
    history?: CredentialHistoryEntry[]
  }
>

export interface CredentialRequestResult {
  status?: { username?: string; password?: string; issuedAt?: string }
}

// The Credential API serves no watch, so the list is refreshed on a timer.
const POLL_INTERVAL_MS = 10_000

export function useApplicationCredentials(
  ad: ApplicationDefinition | undefined,
  instance: ApplicationInstance | undefined,
  namespace: string | undefined,
) {
  const kind = ad?.spec?.application.kind
  const name = instance?.metadata.name
  const query = useK8sList<Credential>(
    { apiGroup: CREDENTIALS_GROUP, apiVersion: CREDENTIALS_VERSION, plural: "credentials", namespace },
    {
      enabled: !!namespace && !!kind && !!name,
      watch: false,
      refetchInterval: POLL_INTERVAL_MS,
      retry: false,
    },
  )
  // 404: the cluster predates the Credential API; 403: the user cannot list it.
  const unavailable = query.error instanceof K8sApiError && [403, 404].includes(query.error.status)
  const credentials = (query.data?.items ?? [])
    .filter((c) => {
      const app = c.spec?.application
      return app?.apiGroup === APPS_GROUP && app.kind === kind && app.name === name
    })
    .sort((a, b) => (a.spec?.user ?? "").localeCompare(b.spec?.user ?? ""))
  return {
    credentials: unavailable ? [] : credentials,
    isLoading: query.isLoading,
    error: unavailable ? null : query.error,
    refetch: query.refetch,
  }
}

/**
 * Mint or revoke through the Credential's subresource. Called on the client
 * directly rather than through a mutation hook: React Query keeps a mutation's
 * result in its cache, and a minted password must not outlive the dialog.
 */
export function runCredentialOperation(
  client: K8sClient,
  namespace: string,
  credential: Credential,
  operation: CredentialOperation,
): Promise<CredentialRequestResult> {
  return client.subresource<CredentialRequestResult>(
    CREDENTIALS_GROUP,
    CREDENTIALS_VERSION,
    "credentials",
    credential.metadata.name,
    operation,
    namespace,
    {
      apiVersion: `${CREDENTIALS_GROUP}/${CREDENTIALS_VERSION}`,
      kind: "CredentialRequest",
      spec: { preconditions: { resourceVersion: credential.metadata.resourceVersion ?? "" } },
    },
    "POST",
  )
}
