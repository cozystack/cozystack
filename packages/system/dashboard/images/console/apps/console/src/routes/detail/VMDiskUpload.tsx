import { useRef, useState } from "react"
import { Upload } from "lucide-react"
import { Button } from "@cozystack/ui"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"
import { useAppConfig } from "../../lib/config.ts"
import { vmSubstrate } from "../../lib/vm-substrate.ts"
import { imageAccept, uploadImage, volumeUploadPath, type VolumeStatus } from "../../lib/image-upload.ts"

// On the Proxmox substrate a vm-disk with source.upload is a kubemox
// VirtualMachineVolume that waits, with status.uploadTarget set, for an
// image file. This button sends one through the console proxy's upload
// subresource; kubemox imports it and clears the target, which hides the
// button again. Tenant access to the subresource is granted by the
// cozy:tenant:use ClusterRole (verb "create"). On KubeVirt the upload goes
// through virtctl and CDI, and there is nothing here.

export function VMDiskUpload({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  // The VirtualMachineVolume is named after the vm-disk release, as the
  // kubemox VirtualMachine is after the vm-instance one.
  const volumeName = `${releasePrefix(ad)}${instance.metadata.name}`
  const proxmox = vmSubstrate(useAppConfig()) === "proxmox"

  const { data: volumes } = useK8sList<K8sResource<unknown, VolumeStatus>>(
    { apiGroup: "proxmox.alperen.cloud", apiVersion: "v1alpha1", plural: "virtualmachinevolumes", namespace: ns },
    { enabled: proxmox && !!ns, fieldSelector: `metadata.name=${volumeName}` },
  )
  const target = volumes?.items[0]?.status?.uploadTarget

  const input = useRef<HTMLInputElement>(null)
  const [progress, setProgress] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  if (!proxmox || !target) return null

  const send = async (file: File) => {
    setError(null)
    setProgress(0)
    try {
      await uploadImage(volumeUploadPath(ns, volumeName, file.name), file, setProgress)
      setDone(true)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setProgress(null)
    }
  }

  return (
    <div className="flex items-center gap-2">
      <input
        ref={input}
        type="file"
        accept={imageAccept(target.content)}
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0]
          e.target.value = ""
          if (file) void send(file)
        }}
      />
      {progress !== null ? (
        <span className="text-sm text-slate-500">Uploading… {Math.round(progress * 100)}%</span>
      ) : done ? (
        <span className="text-sm text-slate-500">Uploaded; importing</span>
      ) : null}
      {error && <span className="text-sm text-red-600">Upload failed: {error}</span>}
      <Button
        variant="outline"
        size="sm"
        disabled={progress !== null}
        onClick={() => input.current?.click()}
      >
        <Upload className="size-3.5" /> Upload image
      </Button>
    </div>
  )
}
