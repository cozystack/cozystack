import { useEffect, useRef, useState } from "react"
import { Monitor, Maximize2, Minimize2, Power, RotateCcw, Terminal } from "lucide-react"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"

interface VncTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function VncTab({ ad, instance }: VncTabProps) {
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

  useEffect(() => {
    if (!containerRef.current || appKind !== "VMInstance" || !isRunning) return

    const el = containerRef.current
    while (el.firstChild) el.removeChild(el.firstChild)

    setLoading(true)
    setError(null)
    setConnected(false)

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
            if (!e.detail?.clean) setError(`Connection lost: ${e.detail?.reason ?? "unknown"}`)
          })

          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          rfb.addEventListener("securityfailure", (e: any) => {
            if (rfbRef.current !== rfb) return
            setConnected(false)
            setLoading(false)
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
  const statusLabel = connected ? "Connected" : loading ? "Connecting…" : "Disconnected"

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
