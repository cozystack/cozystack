import { useState } from "react"
import type { FieldProps } from "@rjsf/utils"
import { useK8sList } from "@cozystack/k8s-client"
import { APPS_GROUP, APPS_VERSION } from "@cozystack/types"
import { useTenantContext } from "../lib/tenant-context.tsx"

const IMAGE_PVC_PREFIX = "vm-default-images-"

interface VMDisk {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace: string }
  spec: { storage: string }
}

interface PVC {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace: string }
}

function useVMDiskOptions(tenantNamespace: string | null | undefined) {
  const { data, isLoading } = useK8sList<VMDisk>({
    apiGroup: APPS_GROUP,
    apiVersion: APPS_VERSION,
    plural: "vmdisks",
    namespace: tenantNamespace ?? undefined,
  })
  return { disks: data?.items ?? [], isLoading }
}

function useImageOptions() {
  const { data, isLoading } = useK8sList<PVC>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "persistentvolumeclaims",
    namespace: "cozy-public",
  })
  const images = (data?.items ?? [])
    .filter((pvc) => pvc.metadata.name.startsWith(IMAGE_PVC_PREFIX))
    .map((pvc) => pvc.metadata.name.slice(IMAGE_PVC_PREFIX.length))
  return { images, isLoading }
}

export function SourceField(props: FieldProps) {
  const { schema, formData, onChange, name, required, idSchema } = props
  const properties = (schema as any).properties || {}
  const options = Object.keys(properties)
  const { tenantNamespace } = useTenantContext()
  const { disks, isLoading: disksLoading } = useVMDiskOptions(tenantNamespace)
  const { images, isLoading: imagesLoading } = useImageOptions()

  // Determine which option is currently selected
  const currentOption = formData
    ? options.find((opt: string) => formData[opt] !== undefined && formData[opt] !== null)
    : undefined

  const [selected, setSelected] = useState<string | undefined>(currentOption)

  const handleSelect = (option: string) => {
    setSelected(option)
    const prop = properties[option]
    // If the property is an empty object marker (like upload: {}), set it to {}
    // Otherwise initialize with default value or empty object
    const defaultValue =
      prop && typeof prop === "object" && Object.keys(prop.properties || {}).length === 0
        ? {}
        : prop && typeof prop === "object" && prop.type === "object"
          ? {}
          : ""
    onChange({ [option]: defaultValue })
  }

  const handleClear = () => {
    setSelected(undefined)
    onChange(undefined)
  }

  const renderFieldInput = (option: string) => {
    const prop = properties[option] as any
    if (!prop || typeof prop !== "object") return null

    // If it's an empty object marker (like upload: {}), show confirmation message
    if (Object.keys(prop.properties || {}).length === 0) {
      return (
        <div className="ml-6 mt-2 rounded-md bg-blue-50 border border-blue-200 p-3">
          <p className="text-sm text-blue-900 font-medium">
            ✓ {option} selected
          </p>
          <p className="text-xs text-blue-700 mt-1">
            {prop.description || "No additional configuration needed"}
          </p>
          {option === "upload" && (
            <p className="text-xs text-blue-600 mt-2">
              After creating the disk, you can upload an image using the UI or virtctl command.
            </p>
          )}
        </div>
      )
    }

    // Render input fields for the selected option
    const subProps = prop.properties || {}
    return (
      <div className="ml-6 mt-2 space-y-2">
        {Object.entries(subProps).map(([key, subProp]: [string, any]) => {
          const currentValue = (formData?.[option] as Record<string, string>)?.[key] || ""
          const handleChange = (val: string) => {
            onChange({
              [option]: {
                ...(formData?.[option] as Record<string, unknown>),
                [key]: val,
              },
            })
          }

          const isDiskName = option === "disk" && key === "name"
          const isImageName = option === "image" && key === "name"

          return (
            <div key={key} className="flex flex-col gap-1">
              <label className="text-sm font-medium text-slate-700">
                {subProp.title || key}
                {prop.required?.includes(key) && <span className="text-red-500 ml-1">*</span>}
              </label>
              {subProp.description && (
                <p className="text-xs text-slate-500">{subProp.description}</p>
              )}
              {isDiskName ? (
                <select
                  value={currentValue}
                  onChange={(e) => handleChange(e.target.value)}
                  disabled={disksLoading}
                  className="w-full rounded-lg border border-slate-300 bg-white pl-3 pr-8 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:opacity-50"
                >
                  <option value="">-- Select disk --</option>
                  {disksLoading ? (
                    <option value="" disabled>Loading...</option>
                  ) : disks.length === 0 ? (
                    <option value="" disabled>No disks available</option>
                  ) : (
                    disks.map((disk) => (
                      <option key={disk.metadata.name} value={disk.metadata.name}>
                        {disk.metadata.name} ({disk.spec.storage})
                      </option>
                    ))
                  )}
                </select>
              ) : isImageName ? (
                <select
                  value={currentValue}
                  onChange={(e) => handleChange(e.target.value)}
                  disabled={imagesLoading}
                  className="w-full rounded-lg border border-slate-300 bg-white pl-3 pr-8 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:opacity-50"
                >
                  <option value="">-- Select image --</option>
                  {imagesLoading ? (
                    <option value="" disabled>Loading...</option>
                  ) : images.length === 0 ? (
                    <option value="" disabled>No images available</option>
                  ) : (
                    images.map((img) => (
                      <option key={img} value={img}>{img}</option>
                    ))
                  )}
                </select>
              ) : (
                <input
                  type="text"
                  value={currentValue}
                  onChange={(e) => handleChange(e.target.value)}
                  placeholder={subProp.title || key}
                  className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
                />
              )}
            </div>
          )
        })}
      </div>
    )
  }

  return (
    <div className="field">
      <label className="control-label mb-2 block">
        {name}
        {required && <span className="required ml-1">*</span>}
      </label>
      {schema.description && <p className="field-description mb-3">{schema.description}</p>}

      <div className="space-y-2">
        {options.map((option: string) => {
          const prop = properties[option] as any
          const optionDescription =
            typeof prop === "object" && prop ? prop.description : undefined
          const isSelected = selected === option

          return (
            <div key={option} className="rounded-lg border border-slate-200 p-3">
              <label className="flex items-start gap-2 cursor-pointer">
                <input
                  type="radio"
                  name={`${idSchema.$id}-source`}
                  checked={isSelected}
                  onChange={() => handleSelect(option)}
                  className="mt-0.5 size-4 rounded-full border-slate-300 text-blue-600 focus:ring-blue-500"
                />
                <div className="flex-1">
                  <div className="text-sm font-medium text-slate-900">{option}</div>
                  {optionDescription && (
                    <div className="text-xs text-slate-500 mt-0.5">{optionDescription}</div>
                  )}
                  {isSelected && renderFieldInput(option)}
                </div>
              </label>
            </div>
          )
        })}
        {selected && (
          <button
            type="button"
            onClick={handleClear}
            className="mt-2 text-xs text-slate-500 hover:text-slate-700"
          >
            Clear selection
          </button>
        )}
      </div>
    </div>
  )
}
