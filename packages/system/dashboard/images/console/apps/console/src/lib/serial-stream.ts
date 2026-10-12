/**
 * Byte stream to a VM's serial console.
 *
 * KubeVirt exposes the guest's serial port as a websocket subresource. Unlike
 * the VNC console it carries text, so a paste is one write rather than an
 * emulated key press per character, and the guest's output arrives as text the
 * user can select and copy.
 *
 * virt-api negotiates the `plain.kubevirt.io` subprotocol and reads binary
 * frames only: a text frame is accepted by the socket and then silently
 * dropped, which looks exactly like a dead console.
 */

const SUBPROTOCOL = "plain.kubevirt.io"

export interface SerialSocket {
  binaryType: string
  onopen: (() => void) | null
  onclose: ((event: { code: number; reason: string }) => void) | null
  onerror: (() => void) | null
  onmessage: ((event: { data: unknown }) => void) | null
  send(data: Uint8Array): void
  close(): void
}

export interface SerialStreamHandlers {
  onData?: (text: string) => void
  onOpen?: () => void
  onClose?: (event: { code: number; reason: string }) => void
}

export interface SerialStream {
  send(text: string): void
  close(): void
}

export function serialConsoleUrl(
  protocol: string,
  host: string,
  namespace: string,
  vmName: string,
): string {
  const scheme = protocol === "https:" ? "wss:" : "ws:"
  return `${scheme}//${host}/k8s/apis/subresources.kubevirt.io/v1/namespaces/${namespace}/virtualmachineinstances/${vmName}/console`
}

/**
 * Frames arrive as ArrayBuffer in the browser, but a proxy or a test harness
 * may hand over a view or an object from another realm, where `instanceof`
 * is false for the very same type.
 */
function toBytes(data: unknown): Uint8Array | null {
  if (ArrayBuffer.isView(data)) {
    const view = data as ArrayBufferView
    return new Uint8Array(view.buffer, view.byteOffset, view.byteLength)
  }
  if (Object.prototype.toString.call(data) === "[object ArrayBuffer]") {
    return new Uint8Array(data as ArrayBuffer)
  }
  return null
}

type SocketFactory = (url: string, protocols: string[]) => SerialSocket

const defaultFactory: SocketFactory = (url, protocols) =>
  new WebSocket(url, protocols) as unknown as SerialSocket

export function openSerialStream(
  url: string,
  handlers: SerialStreamHandlers = {},
  createSocket: SocketFactory = defaultFactory,
): SerialStream {
  const socket = createSocket(url, [SUBPROTOCOL])
  socket.binaryType = "arraybuffer"

  const encoder = new TextEncoder()
  // Streaming: a UTF-8 character can straddle two frames, and decoding each
  // frame on its own would turn it into replacement characters.
  const decoder = new TextDecoder("utf-8", { fatal: false })

  // The socket is not writable until it opens, and WebSocket.send() throws
  // InvalidStateError while it is still CONNECTING. The terminal is live from
  // the moment it is rendered, so anything typed before then is held and
  // flushed rather than thrown away.
  let state: "connecting" | "open" | "closed" = "connecting"
  let pending = ""
  let errored = false

  const finish = (event: { code: number; reason: string }) => {
    if (state === "closed") return
    state = "closed"
    pending = ""
    handlers.onClose?.(event)
  }

  socket.onopen = () => {
    state = "open"
    if (pending) {
      socket.send(encoder.encode(pending))
      pending = ""
    }
    handlers.onOpen?.()
  }
  socket.onmessage = (event) => {
    if (state === "closed") return
    const bytes = toBytes(event.data)
    if (!bytes) return
    handlers.onData?.(decoder.decode(bytes, { stream: true }))
  }
  // onerror carries no reason and is always followed by onclose, which does:
  // reporting the synthetic one first would hide "1008 Forbidden" behind
  // "connection error".
  socket.onerror = () => {
    errored = true
  }
  socket.onclose = (event) =>
    finish(errored && event.code === 1005 ? { code: 1006, reason: "connection error" } : event)

  return {
    send(text: string) {
      if (state === "closed") return
      if (state === "connecting") {
        pending += text
        return
      }
      socket.send(encoder.encode(text))
    },
    close() {
      state = "closed"
      pending = ""
      socket.close()
    },
  }
}
