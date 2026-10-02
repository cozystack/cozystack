import { useEffect, useRef, useState } from "react"
import { Monitor, RotateCcw, Terminal as TerminalIcon } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"
import { useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"
import { pastesWithMeta } from "../../lib/platform.ts"
import { openSerialStream, serialConsoleUrl, type SerialStream } from "../../lib/serial-stream.ts"

type ConnectionPhase = "connecting" | "connected" | "closed"

async function copySelection(text: string) {
  try {
    await navigator.clipboard?.writeText(text)
  } catch {
    // No clipboard permission, or a page served over plain http where the API
    // is absent. The selection stays selected, so the browser's own copy works.
  }
}

interface SerialTabProps {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}

export function SerialTab({ ad, instance }: SerialTabProps) {
  const ns = instance.metadata.namespace
  const appKind = ad.spec?.application.kind
  // Same resolution as VncTab and VMPowerControls: the cozystack app name maps
  // to "<release.prefix><name>", and all three must target the same object.
  const vmName = `${releasePrefix(ad)}${instance.metadata.name}`

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

  const hostRef = useRef<HTMLDivElement>(null)
  const [phase, setPhase] = useState<ConnectionPhase>("connecting")
  const [error, setError] = useState<string | null>(null)
  const [connectionKey, setConnectionKey] = useState(0)

  useEffect(() => {
    const host = hostRef.current
    if (!host || appKind !== "VMInstance" || !isRunning || !ns) return

    setPhase("connecting")
    setError(null)


    const terminal = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 5000,
      theme: {
        background: "#0d0f14",
        foreground: "#cbd5e1",
        cursor: "#cbd5e1",
        selectionBackground: "#334155",
      },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(host)
    fit.fit()

    let stream: SerialStream | null = null

    terminal.attachCustomKeyEventHandler((event) => {
      if (event.type !== "keydown") return true

      // Copy keeps the terminal's own meaning of Ctrl+C (interrupt) intact, so
      // copying goes through the chord terminals use for it.
      const copyChord = (event.ctrlKey && event.shiftKey) || event.metaKey
      if (copyChord && event.code === "KeyC" && terminal.hasSelection()) {
        void copySelection(terminal.getSelection())
        return false
      }

      // Cmd+V and Shift+Insert paste by themselves, but Ctrl+V does not:
      // xterm turns Ctrl with a letter into a control byte and cancels the
      // keydown, so the browser's paste never runs and the guest receives SYN
      // (0x16) instead. Handing the event back to the browser costs the
      // terminal's quoted-insert, which is the trade worth making where
      // Ctrl+V is the paste chord. On a Mac it is not — the browser pastes on
      // Cmd+V — so there the terminal keeps the key.
      if (
        !pastesWithMeta() &&
        event.ctrlKey &&
        !event.shiftKey &&
        !event.altKey &&
        event.code === "KeyV"
      ) {
        return false
      }

      return true
    })

    stream = openSerialStream(
      serialConsoleUrl(window.location.protocol, window.location.host, ns!, vmName),
      {
        onOpen: () => {
          setPhase("connected")
          terminal.focus()
        },
        onData: (text) => terminal.write(text),
        onClose: ({ code, reason }) => {
          setPhase("closed")
          if (code !== 1000) setError(reason || `connection closed (${code})`)
        },
      },
    )

    const input = terminal.onData((data) => stream?.send(data))

    const resize = new ResizeObserver(() => fit.fit())
    resize.observe(host)

    return () => {
      resize.disconnect()
      input.dispose()
      stream?.close()
      terminal.dispose()
    }
  }, [appKind, ns, vmName, isRunning, connectionKey])

  if (appKind !== "VMInstance") {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <div className="flex flex-col items-center gap-2 text-center">
          <TerminalIcon className="h-8 w-8 text-slate-300" />
          <p className="text-sm text-slate-500">
            The serial console is only available for VMInstance.
          </p>
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
            {powerStatus ? ` (status: ${powerStatus})` : ""}. Start it to use the serial console.
          </p>
        </div>
      </div>
    )
  }

  const connected = phase === "connected"
  const statusColor =
    phase === "connected" ? "bg-emerald-500" : phase === "closed" ? "bg-red-500" : "bg-amber-400"
  const statusLabel =
    phase === "connected" ? "Connected" : phase === "closed" ? "Disconnected" : "Connecting…"

  return (
    <div className="flex h-full flex-col p-4">
      <div className="flex flex-1 flex-col overflow-hidden rounded-xl shadow-[0_4px_24px_rgba(0,0,0,0.12)] ring-1 ring-slate-900/8">
        <div className="flex shrink-0 items-center justify-between bg-[#1e2330] px-3 py-2">
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
              <TerminalIcon className="h-3 w-3" />
              <span className="font-mono">{instance.metadata.name}</span>
            </div>
          </div>

          <button
            type="button"
            onClick={() => setConnectionKey((k) => k + 1)}
            title="Reconnect"
            className="flex cursor-pointer items-center gap-1 rounded px-1.5 py-1 text-slate-500 transition-colors hover:bg-slate-700/60 hover:text-slate-200"
          >
            <RotateCcw className="h-3.5 w-3.5" />
          </button>
        </div>

        <div className="relative flex-1 overflow-hidden bg-[#0d0f14]">
          {error && !connected && (
            <div className="absolute inset-x-0 top-0 z-10 bg-red-950/70 px-3 py-1.5 text-[11px] text-red-300">
              {error}
            </div>
          )}
          {/* The terminal sizes itself to this box, so the breathing room has
              to be padding on it rather than a margin further out. */}
          <div ref={hostRef} className="h-full w-full px-3 pt-2 pb-3" />
        </div>
      </div>
    </div>
  )
}
