/**
 * The image a vm-disk with source.upload waits for, on the Proxmox substrate.
 * kubemox publishes where it expects the file in the VirtualMachineVolume's
 * status.uploadTarget; the platform's console proxy takes the file as one
 * POST body on the volume's upload subresource (under the dashboard's /k8s/
 * proxy, whose body limit is lifted for this) and streams it to the
 * hypervisor. The response arrives once the hypervisor has the whole file.
 */

/** kubemox's status.uploadTarget, as far as the button needs it. */
export interface VolumeUploadTarget {
  /** import for a disk image, iso for an optical one */
  content?: string
}

export interface VolumeStatus {
  uploadTarget?: VolumeUploadTarget
}

/** The file extensions the hypervisor imports under each content type. */
export function imageAccept(content: string | undefined): string {
  return content === "iso" ? ".iso,.img" : ".qcow2,.raw,.vmdk,.ova"
}

export function volumeUploadPath(namespace: string, volumeName: string, filename: string): string {
  return `/k8s/apis/subresources.proxmox.cozystack.io/v1alpha1/namespaces/${namespace}/virtualmachinevolumes/${volumeName}/upload?filename=${encodeURIComponent(filename)}`
}

/**
 * Sends file to path, reporting progress as a fraction of its bytes. fetch has
 * no upload progress, so this is an XMLHttpRequest. Resolves once the proxy
 * answers 2xx; rejects with the message of the Status the proxy returned.
 */
export function uploadImage(
  path: string,
  file: File,
  onProgress: (fraction: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open("POST", path)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total)
    }
    xhr.onerror = () => reject(new Error("the upload was interrupted"))
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) return resolve()
      let message = xhr.responseText || `HTTP ${xhr.status}`
      try {
        message = (JSON.parse(xhr.responseText) as { message?: string }).message ?? message
      } catch {
        // not a Status; the text is the message
      }
      reject(new Error(message))
    }
    xhr.send(file)
  })
}
