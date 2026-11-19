import type { K8sResource } from "@cozystack/k8s-client"

export interface CommandItem {
  id: string
  label: string
  description?: string
  icon?: React.ReactNode
  group?: string
  drilldown?: boolean
  keywords?: string[]
  onSelect: () => void
}

export type NavigationLevel =
  | { type: "root" }
  | {
      type: "resource"
      plural: string
      label: string
      icon?: string
    }
  | {
      type: "instance"
      plural: string
      instance: K8sResource
      label: string
      resourceLabel: string
      icon?: string
    }
