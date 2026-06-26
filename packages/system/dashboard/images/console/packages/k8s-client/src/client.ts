export interface K8sClientConfig {
  baseUrl?: string
  getToken?: () => Promise<string>
}

export class K8sApiError extends Error {
  status: number
  body: unknown

  constructor(status: number, body: unknown) {
    const msg =
      typeof body === "object" && body !== null && "message" in body
        ? (body as { message: string }).message
        : typeof body === "string" && body.length > 0 && body.length < 200
          ? body
          : `Server returned ${status}`
    super(msg)
    this.name = "K8sApiError"
    this.status = status
    this.body = body
  }
}

export class K8sClient {
  private baseUrl: string
  private getToken?: () => Promise<string>

  constructor(config: K8sClientConfig = {}) {
    this.baseUrl = config.baseUrl ?? ""
    this.getToken = config.getToken
  }

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...(init?.headers as Record<string, string>),
    }

    if (this.getToken) {
      const token = await this.getToken()
      headers["Authorization"] = `Bearer ${token}`
    }

    let res: Response
    try {
      res = await fetch(`${this.baseUrl}${path}`, {
        ...init,
        headers,
      })
    } catch {
      throw new K8sApiError(0, "Unable to connect to the server")
    }

    if (!res.ok) {
      let body: unknown
      try {
        const text = await res.text()
        try {
          body = JSON.parse(text)
        } catch {
          body = text
        }
      } catch {
        body = `Server returned ${res.status} ${res.statusText}`
      }
      throw new K8sApiError(res.status, body)
    }

    if (res.status === 204) return undefined as T
    // Some endpoints (e.g. KubeVirt action subresources like
    // virtualmachines/{name}/restart) return 2xx with an empty body;
    // res.json() would throw "Unexpected end of JSON input" on "".
    const text = await res.text()
    if (!text) return undefined as T
    return JSON.parse(text) as T
  }

  private buildPath(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    namespace?: string,
    name?: string,
  ): string {
    const base = apiGroup
      ? `/apis/${apiGroup}/${apiVersion}`
      : `/api/${apiVersion}`
    const ns = namespace ? `/namespaces/${namespace}` : ""
    const resource = `/${plural}`
    const item = name ? `/${name}` : ""
    return `${base}${ns}${resource}${item}`
  }

  list<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    namespace?: string,
    search?: { labelSelector?: string; fieldSelector?: string },
  ): Promise<K8sList<T>> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace)
    const params = new URLSearchParams()
    if (search?.labelSelector) params.set("labelSelector", search.labelSelector)
    if (search?.fieldSelector) params.set("fieldSelector", search.fieldSelector)
    const qs = params.toString()
    return this.request<K8sList<T>>(qs ? `${path}?${qs}` : path)
  }

  get<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    name: string,
    namespace?: string,
  ): Promise<T> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace, name)
    return this.request<T>(path)
  }

  create<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    body: T,
    namespace?: string,
  ): Promise<T> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace)
    return this.request<T>(path, {
      method: "POST",
      body: JSON.stringify(body),
    })
  }

  update<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    name: string,
    body: T,
    namespace?: string,
  ): Promise<T> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace, name)
    return this.request<T>(path, {
      method: "PUT",
      body: JSON.stringify(body),
    })
  }

  patch<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    name: string,
    patch: unknown,
    namespace?: string,
    type: "merge" | "strategic" | "json" = "merge",
  ): Promise<T> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace, name)
    const contentType =
      type === "merge"
        ? "application/merge-patch+json"
        : type === "strategic"
          ? "application/strategic-merge-patch+json"
          : "application/json-patch+json"
    return this.request<T>(path, {
      method: "PATCH",
      headers: { "Content-Type": contentType },
      body: JSON.stringify(patch),
    })
  }

  delete(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    name: string,
    namespace?: string,
  ): Promise<unknown> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace, name)
    return this.request(path, { method: "DELETE" })
  }

  /**
   * Call a resource subresource (e.g. KubeVirt's
   * subresources.kubevirt.io virtualmachines/{name}/start|stop|restart).
   * Defaults to PUT, which is what the KubeVirt action subresources expect;
   * pass an empty object as body when the subresource takes no options.
   */
  subresource<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    name: string,
    subresource: string,
    namespace?: string,
    body?: unknown,
    method: "PUT" | "POST" = "PUT",
  ): Promise<T> {
    const path = `${this.buildPath(apiGroup, apiVersion, plural, namespace, name)}/${subresource}`
    return this.request<T>(path, {
      method,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  }

  dryRunCreate<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    body: T,
    namespace?: string,
  ): Promise<T> {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace)
    return this.request<T>(`${path}?dryRun=All`, {
      method: "POST",
      body: JSON.stringify(body),
    })
  }

  getApiGroups(): Promise<APIGroupList> {
    return this.request<APIGroupList>("/apis")
  }

  watch<T>(
    apiGroup: string,
    apiVersion: string,
    plural: string,
    namespace: string | undefined,
    resourceVersion: string,
    onEvent: (event: WatchEvent<T>) => void,
    onError?: (error: Error) => void,
    search?: { labelSelector?: string; fieldSelector?: string },
  ): () => void {
    const path = this.buildPath(apiGroup, apiVersion, plural, namespace)
    const params = new URLSearchParams({
      watch: "true",
      resourceVersion,
      allowWatchBookmarks: "true",
    })
    if (search?.labelSelector) params.set("labelSelector", search.labelSelector)
    if (search?.fieldSelector) params.set("fieldSelector", search.fieldSelector)

    const controller = new AbortController()

    const run = async () => {
      try {
        const headers: Record<string, string> = {}
        if (this.getToken) {
          const token = await this.getToken()
          headers["Authorization"] = `Bearer ${token}`
        }

        const res = await fetch(`${this.baseUrl}${path}?${params}`, {
          headers,
          signal: controller.signal,
        })

        if (!res.ok) {
          throw new K8sApiError(
            res.status,
            await res.json().catch(() => res.statusText),
          )
        }
        if (!res.body) throw new Error("No response body for watch")

        const reader = res.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ""

        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split("\n")
          buffer = lines.pop() ?? ""

          for (const line of lines) {
            if (!line.trim()) continue
            try {
              const event = JSON.parse(line) as WatchEvent<T>
              onEvent(event)
            } catch {
              // skip malformed lines
            }
          }
        }

        if (!controller.signal.aborted) {
          onError?.(new Error("Watch stream ended"))
        }
      } catch (err) {
        if (controller.signal.aborted) return
        onError?.(err instanceof Error ? err : new Error(String(err)))
      }
    }

    run()

    return () => controller.abort()
  }
}

export interface K8sMetadata {
  name: string
  namespace?: string
  uid?: string
  resourceVersion?: string
  creationTimestamp?: string
  deletionTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  generation?: number
  finalizers?: string[]
  ownerReferences?: K8sOwnerReference[]
}

export interface K8sOwnerReference {
  apiVersion: string
  kind: string
  name: string
  uid: string
  controller?: boolean
  blockOwnerDeletion?: boolean
}

export interface K8sResource<Spec = unknown, Status = unknown> {
  apiVersion: string
  kind: string
  metadata: K8sMetadata
  spec?: Spec
  status?: Status
}

export interface K8sList<T> {
  apiVersion: string
  kind: string
  metadata: { resourceVersion?: string }
  items: T[]
}

export interface K8sCondition {
  type: string
  status: "True" | "False" | "Unknown"
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export interface WatchEvent<T> {
  type: "ADDED" | "MODIFIED" | "DELETED" | "BOOKMARK" | "ERROR"
  object: T
}

export interface APIGroupVersion {
  groupVersion: string
  version: string
}

export interface APIGroup {
  name: string
  versions: APIGroupVersion[]
  preferredVersion?: APIGroupVersion
}

export interface APIGroupList {
  kind: string
  apiVersion: string
  groups: APIGroup[]
}
