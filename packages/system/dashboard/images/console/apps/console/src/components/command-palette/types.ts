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
      instance: any
      label: string
      resourceLabel: string
      icon?: string
    }
