import { useCallback, useEffect, useRef, useState } from "react"
import { Monitor, Maximize2, Minimize2, Power, RotateCcw, Terminal } from "lucide-react"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"
import { pastesWithMeta } from "../../lib/platform.ts"
import { planKeystrokes } from "../../lib/vnc-keymap.ts"
import {
  typeKeystrokes,
  DEFAULT_KEY_DELAY_MS,
  LATE_MODIFIER_WINDOW_MS,
  type KeySender,
} from "../../lib/vnc-typing.ts"

// How long a one-off paste notice stays in the toolbar before the status
// returns to the connection state.
const PASTE_NOTICE_MS = 5000

// How long the hidden sink may hold the keyboard while waiting for the paste
// the shortcut should have produced.
const SINK_FOCUS_TIMEOUT_MS = 300

type PasteState =
  | { kind: "idle" }
  | { kind: "typing"; typed: number; total: number }
  | { kind: "blocked" }
  | { kind: "skipped"; count: number }
  | { kind: "interrupted"; typed: number }

interface VncTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
  /** Injectable so tests do not run at the pace of a real keyboard. */
  keyDelayMs?: number
}

export function VncTab({ ad, instance, keyDelayMs = DEFAULT_KEY_DELAY_MS }: VncTabProps) {
  const ns = instance.metadata.namespace
  const appKind = ad.spec?.application.kind
  // The cozystack app name (e.g. "demo-vm") maps to the KubeVirt VirtualMachine
  // / VirtualMachineInstance named "<release.prefix><name>". releasePrefix()
  // discovers the prefix from the ApplicationDefinition (falling back to
  // "<singular>-") so this resolves identically to VMPowerControls — both must
  // target the same object, so neither may hardcode the prefix.
  const vmName = `${releasePrefix(ad)}${instance.metadata.name}`

  // Don't open a VNC websocket unless the VM is actually running — there is no
  // VirtualMachineInstance to attach to otherwise, and the socket would just
  // error out. List the VirtualMachine by a metadata.name field-selector so the
  // useK8sList watch layer streams power-state transitions live — no poll.
  const { data: vmList, isLoading: vmLoading } = useK8sList<
    K8sResource<unknown, { printableStatus?: string }>
  >(
    {
      apiGroup: "kubevirt.io",
      apiVersion: "v1",
      plural: "virtualmachines",
      namespace: ns ?? "",
    },
    {
      enabled: appKind === "VMInstance" && !!ns,
      fieldSelector: `metadata.name=${vmName}`,
    },
  )
  const powerStatus = vmList?.items[0]?.status?.printableStatus
  const isRunning = powerStatus === "Running"

  const containerRef = useRef<HTMLDivElement>(null)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any -- noVNC RFB has no bundled types
  const rfbRef = useRef<any>(null)
  const [connected, setConnected] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [fullscreen, setFullscreen] = useState(false)
  const [connectionKey, setConnectionKey] = useState(0)
  const [desktopSize, setDesktopSize] = useState<{ width: number; height: number } | null>(null)
  const [paste, setPaste] = useState<PasteState>({ kind: "idle" })
  const senderRef = useRef<KeySender | null>(null)
  const pastingRef = useRef(false)
  const pasteAbortRef = useRef<AbortController | null>(null)
  // When the shortcut was pressed, so the paste knows how much of noVNC's
  // modifier-delivery window is still ahead of it.
  const shortcutAtRef = useRef(0)
  const pasteSinkRef = useRef<HTMLTextAreaElement>(null)
  const sinkTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!containerRef.current || appKind !== "VMInstance" || !isRunning) return

    const el = containerRef.current
    while (el.firstChild) el.removeChild(el.firstChild)

    setLoading(true)
    setError(null)
    setConnected(false)
    setPaste({ kind: "idle" })
    senderRef.current = null

    const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:"
    const wsUrl = `${wsProtocol}//${window.location.host}/k8s/apis/subresources.kubevirt.io/v1/namespaces/${ns}/virtualmachineinstances/${vmName}/vnc`

    // Prevent stale import resolutions from firing after cleanup or reconnect
    let cancelled = false

    import("@novnc/novnc/lib/rfb")
      .then((module) => {
        if (cancelled || !containerRef.current) return
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const RFB = (module as any).default?.default ?? module.default
        try {
          const rfb = new RFB(el, wsUrl, { credentials: {} })
          rfb.scaleViewport = true
          rfb.resizeSession = true

          // Guard each handler: if rfbRef was replaced by a newer session, ignore
          rfb.addEventListener("connect", () => {
            if (rfbRef.current !== rfb) return
            setLoading(false)
            setConnected(true)
            setError(null)
            senderRef.current = {
              sendKey: (keysym: number, code: string, down: boolean) =>
                rfb.sendKey(keysym, code, down),
            }
            requestAnimationFrame(() => {
              const canvas = el.querySelector("canvas")
              if (canvas) setDesktopSize({ width: canvas.width, height: canvas.height })
            })
          })

          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          rfb.addEventListener("disconnect", (e: any) => {
            if (rfbRef.current !== rfb) return
            setConnected(false)
            setLoading(false)
            senderRef.current = null
            if (!e.detail?.clean) setError(`Connection lost: ${e.detail?.reason ?? "unknown"}`)
          })

          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          rfb.addEventListener("securityfailure", (e: any) => {
            if (rfbRef.current !== rfb) return
            setConnected(false)
            setLoading(false)
            senderRef.current = null
            setError(`Security failure: ${e.detail?.status ?? "authentication failed"}`)
          })

          rfbRef.current = rfb
        } catch (err) {
          if (!cancelled) {
            setLoading(false)
            setError(`Failed to initialize VNC: ${(err as Error).message}`)
          }
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setLoading(false)
          setError(`Failed to load VNC library: ${err.message}`)
        }
      })

    return () => {
      cancelled = true
      // Stop an in-flight paste explicitly. noVNC's "disconnect" event arrives
      // asynchronously, by which point the guard below has already dropped it,
      // so senderRef would otherwise stay non-null and the typing loop would
      // run on past the unmount.
      pasteAbortRef.current?.abort()
      senderRef.current = null
      if (rfbRef.current) {
        try {
          rfbRef.current.disconnect()
        } catch {
          /* ignore: socket may already be closed */
        }
        rfbRef.current = null
      }
    }
  }, [appKind, ns, vmName, isRunning, connectionKey])

  useEffect(() => {
    const handler = () => setFullscreen(!!document.fullscreenElement)
    document.addEventListener("fullscreenchange", handler)
    return () => document.removeEventListener("fullscreenchange", handler)
  }, [])

  // There is no clipboard channel behind this console: qemu only forwards RFB
  // cut-text to a vdagent chardev and virt-launcher attaches none. So a paste
  // is typed, one key at a time, on the layout the guest has active.
  const typeIntoGuest = useCallback(async (text: string) => {
    const sender = senderRef.current
    if (!sender || pastingRef.current) return
    pastingRef.current = true
    try {
      const { keystrokes, unsupported } = planKeystrokes(text)

      // Typing the rest would run a different command than the one pasted: a
      // dropped character inside a path still leaves a valid path, and the
      // newline after it still submits. "rm -rf /srv/данные" becomes
      // "rm -rf /srv". So nothing is sent, and the notice comes first.
      if (unsupported.length > 0) {
        setPaste({ kind: "skipped", count: unsupported.length })
        return
      }

      if (keystrokes.length > 0) {
        const controller = new AbortController()
        pasteAbortRef.current = controller
        setPaste({ kind: "typing", typed: 0, total: keystrokes.length })
        const elapsed = shortcutAtRef.current ? Date.now() - shortcutAtRef.current : 0
        const result = await typeKeystrokes(sender, keystrokes, {
          delayMs: keyDelayMs,
          settleMs: Math.max(0, LATE_MODIFIER_WINDOW_MS - elapsed),
          signal: controller.signal,
          // Identity rather than liveness. The cleanup below aborts an
          // in-flight paste, so a replacement session cannot be reached in
          // practice; asking whether this sender is still the current one
          // costs nothing and does not depend on that ordering holding.
          isConnected: () => senderRef.current === sender,
          onProgress: (typed, total) => setPaste({ kind: "typing", typed, total }),
        })
        pasteAbortRef.current = null
        if (result.stopped !== "done") {
          setPaste({ kind: "interrupted", typed: result.typed })
          return
        }
      }
      setPaste({ kind: "idle" })
    } finally {
      pastingRef.current = false
    }
  }, [keyDelayMs])

  useEffect(() => {
    if (!connected) return

    const handler = (e: KeyboardEvent) => {
      const isV = e.code === "KeyV" || e.key.toLowerCase() === "v"
      if (!isV || e.altKey || e.shiftKey) return
      // Only the chord this platform actually pastes with. Taking the other
      // one buys nothing — the browser does not paste on Ctrl+V on a Mac —
      // and costs the guest a key it has its own use for, such as visual
      // block in vim or a paste from the guest's own clipboard on Windows.
      if (pastesWithMeta() ? !e.metaKey : !e.ctrlKey) return
      // Only when the console itself holds the keyboard — a paste into the
      // page's own inputs must keep working.
      const el = containerRef.current
      const sink = pasteSinkRef.current
      if (!el || !sink || !document.activeElement) return
      if (!el.contains(document.activeElement)) return

      // Deliberately no preventDefault: the browser hands the clipboard over
      // for free when the paste lands in a focused text field, while
      // navigator.clipboard.readText() would put a permission prompt between
      // the user and every paste. Moving the focus here, inside the keydown,
      // is what redirects the paste into the sink.
      //
      // stopPropagation is what keeps noVNC from seeing the shortcut: it calls
      // preventDefault() on keydown, which would cancel the paste outright and
      // send the guest a Ctrl+V it cannot use.
      e.stopPropagation()
      shortcutAtRef.current = Date.now()
      sink.value = ""
      sink.focus()

      // An empty or refused clipboard raises no paste event at all, and the
      // console would sit there with the keyboard parked in an invisible box.
      if (sinkTimerRef.current) clearTimeout(sinkTimerRef.current)
      sinkTimerRef.current = setTimeout(() => {
        sinkTimerRef.current = null
        if (document.activeElement !== sink) return
        rfbRef.current?.focus()
        setPaste({ kind: "blocked" })
      }, SINK_FOCUS_TIMEOUT_MS)
    }

    document.addEventListener("keydown", handler, true)
    return () => document.removeEventListener("keydown", handler, true)
  }, [connected])

  useEffect(
    () => () => {
      if (sinkTimerRef.current) clearTimeout(sinkTimerRef.current)
    },
    [],
  )

  const handleSinkPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    e.preventDefault()
    if (sinkTimerRef.current) {
      clearTimeout(sinkTimerRef.current)
      sinkTimerRef.current = null
    }
    const text = e.clipboardData?.getData("text/plain") ?? ""
    rfbRef.current?.focus()
    if (text) void typeIntoGuest(text)
  }

  const handleSinkBlur = () => {
    if (pasteSinkRef.current) pasteSinkRef.current.value = ""
  }

  // One-off notices clear themselves; a paste in progress reports until it ends.
  useEffect(() => {
    if (paste.kind === "idle" || paste.kind === "typing") return
    const timer = setTimeout(() => setPaste({ kind: "idle" }), PASTE_NOTICE_MS)
    return () => clearTimeout(timer)
  }, [paste])

  if (appKind !== "VMInstance") {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <div className="flex flex-col items-center gap-2 text-center">
          <Terminal className="h-8 w-8 text-slate-300" />
          <p className="text-sm text-slate-500">VNC is only available for VMInstance.</p>
        </div>
      </div>
    )
  }

  if (vmLoading) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <p className="text-sm text-slate-500">Loading…</p>
      </div>
    )
  }

  if (!isRunning) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <div className="flex flex-col items-center gap-2 text-center">
          <Monitor className="h-8 w-8 text-slate-300" />
          <p className="text-sm text-slate-500">
            The virtual machine is not running
            {powerStatus ? ` (status: ${powerStatus})` : ""}. Start it to use the VNC console.
          </p>
        </div>
      </div>
    )
  }

  const handleFullscreen = () => {
    const wrapper = containerRef.current?.parentElement
    if (!wrapper) return
    if (!document.fullscreenElement) {
      wrapper.requestFullscreen()
    } else {
      document.exitFullscreen()
    }
  }

  const handleReconnect = () => {
    if (rfbRef.current) {
      try {
        rfbRef.current.disconnect()
      } catch {
        /* ignore: socket may already be closed */
      }
      rfbRef.current = null
    }
    setDesktopSize(null)
    setConnectionKey((k) => k + 1)
  }

  const statusColor = connected ? "bg-emerald-500" : loading ? "bg-amber-400" : "bg-red-500"
  const connectionLabel = connected ? "Connected" : loading ? "Connecting…" : "Disconnected"
  const statusLabel =
    paste.kind === "typing"
      ? `Pasting ${paste.typed}/${paste.total}`
      : paste.kind === "blocked"
        ? "Clipboard blocked"
        : paste.kind === "skipped"
          ? `${paste.count} chars not on layout — nothing sent`
          : paste.kind === "interrupted"
            ? `Paste stopped after ${paste.typed}`
            : connectionLabel

  return (
    <div className="flex h-full flex-col p-4">
      {/* Outer panel — shadow bridges the light page and dark terminal */}
      <div className="flex flex-1 flex-col overflow-hidden rounded-xl shadow-[0_4px_24px_rgba(0,0,0,0.12)] ring-1 ring-slate-900/8">
        {/* ── Toolbar ── */}
        <div className="flex shrink-0 items-center justify-between bg-[#1e2330] px-3 py-2">
          {/* Left: status pill + vm name */}
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-1.5">
              <span className="relative flex h-2 w-2">
                {connected && (
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
                )}
                <span className={`relative inline-flex h-2 w-2 rounded-full ${statusColor}`} />
              </span>
              <span className="text-[11px] font-medium tracking-wide text-slate-400">
                {statusLabel}
              </span>
            </div>

            <div className="h-3.5 w-px bg-slate-700" />

            <div className="flex items-center gap-1.5 text-[11px] text-slate-500">
              <Terminal className="h-3 w-3" />
              <span className="font-mono">{instance.metadata.name}</span>
            </div>
          </div>

          {/* Right: action buttons */}
          <div className="flex items-center gap-0.5">
            {connected && (
              <ToolbarButton
                onClick={() => rfbRef.current?.sendCtrlAltDel()}
                title="Ctrl+Alt+Del"
              >
                <Power className="h-3.5 w-3.5" />
              </ToolbarButton>
            )}

            <div className="mx-1 h-3.5 w-px bg-slate-700" />

            <ToolbarButton
              onClick={handleFullscreen}
              title={fullscreen ? "Exit fullscreen" : "Fullscreen"}
            >
              {fullscreen ? (
                <Minimize2 className="h-3.5 w-3.5" />
              ) : (
                <Maximize2 className="h-3.5 w-3.5" />
              )}
            </ToolbarButton>

            <ToolbarButton onClick={handleReconnect} disabled={loading} title="Reconnect">
              <RotateCcw className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`} />
            </ToolbarButton>
          </div>
        </div>

        {/* ── Canvas area ── */}
        <div
          className="relative flex-1 overflow-hidden bg-black"
          style={
            desktopSize
              ? {
                  aspectRatio: `${desktopSize.width} / ${desktopSize.height}`,
                  width: "100%",
                  flex: "unset",
                }
              : undefined
          }
        >
          {/* Loading overlay */}
          {loading && (
            <div className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-3 bg-[#0d0f14]">
              <div className="flex gap-1">
                {[0, 1, 2].map((i) => (
                  <span
                    key={i}
                    className="h-1.5 w-1.5 animate-bounce rounded-full bg-slate-600"
                    style={{ animationDelay: `${i * 0.15}s` }}
                  />
                ))}
              </div>
              <p className="text-[11px] font-medium tracking-widest text-slate-600 uppercase">
                Connecting
              </p>
            </div>
          )}

          {/* Error overlay */}
          {error && !loading && (
            <div className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-4 bg-[#0d0f14] p-8">
              <div className="flex h-10 w-10 items-center justify-center rounded-full bg-red-900/40 ring-1 ring-red-800/60">
                <Terminal className="h-5 w-5 text-red-400" />
              </div>
              <div className="text-center">
                <p className="text-sm font-medium text-red-400">Connection failed</p>
                <p className="mt-1 text-xs text-slate-600">{error}</p>
              </div>
              <button
                type="button"
                onClick={handleReconnect}
                className="flex items-center gap-1.5 rounded-md bg-slate-800 px-3 py-1.5 text-xs font-medium text-slate-300 ring-1 ring-slate-700 transition-colors hover:bg-slate-700 hover:text-white"
              >
                <RotateCcw className="h-3 w-3" /> Reconnect
              </button>
            </div>
          )}

          {/* Invisible sink that catches the browser paste. It sits outside the
              noVNC container, which is emptied on every reconnect. */}
          <textarea
            ref={pasteSinkRef}
            onPaste={handleSinkPaste}
            onBlur={handleSinkBlur}
            tabIndex={-1}
            aria-hidden="true"
            className="pointer-events-none absolute top-0 left-0 h-px w-px resize-none border-0 p-0 opacity-0 outline-none"
          />

          <div ref={containerRef} className="absolute inset-0" />
        </div>
      </div>
    </div>
  )
}

function ToolbarButton({
  onClick,
  title,
  disabled,
  label,
  children,
}: {
  onClick: () => void
  title: string
  disabled?: boolean
  label?: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className="flex cursor-pointer items-center gap-1 rounded px-1.5 py-1 text-slate-500 transition-colors hover:bg-slate-700/60 hover:text-slate-200 disabled:cursor-not-allowed disabled:opacity-30"
    >
      {children}
      {label && <span className="text-[10px] font-medium">{label}</span>}
    </button>
  )
}
