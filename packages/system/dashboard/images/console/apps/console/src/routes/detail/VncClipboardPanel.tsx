import { useEffect, useId, useMemo, useRef, useState } from "react"
import { ClipboardPaste, Copy, Send, X } from "lucide-react"
import {
  KEYBOARD_LAYOUTS,
  LAYOUT_LABELS,
  planKeystrokes,
  type KeyboardLayout,
} from "../../lib/vnc-keymap.ts"
import { DEFAULT_KEY_DELAY_MS, typeKeystrokes, type KeySender } from "../../lib/vnc-typing.ts"

const LAYOUT_STORAGE_KEY = "cozystack.vnc.layout"

function isLayout(value: unknown): value is KeyboardLayout {
  return KEYBOARD_LAYOUTS.some((l) => l === value)
}

function storedLayout(): KeyboardLayout {
  if (typeof window === "undefined") return "en-us"
  try {
    const stored = window.localStorage.getItem(LAYOUT_STORAGE_KEY)
    return isLayout(stored) ? stored : "en-us"
  } catch {
    return "en-us"
  }
}

function describe(char: string): string {
  const codePoint = char.codePointAt(0) ?? 0
  if (codePoint < 0x20 || codePoint === 0x7f) {
    return `U+${codePoint.toString(16).toUpperCase().padStart(4, "0")}`
  }
  return char
}

interface VncClipboardPanelProps {
  /** null while the session is not connected — typing is refused then. */
  sender: KeySender | null
  /** Last text the server pushed over RFB, or null if it pushed none. */
  guestClipboard: string | null
  onClose: () => void
  onTypingFinished?: () => void
  keyDelayMs?: number
}

export function VncClipboardPanel({
  sender,
  guestClipboard,
  onClose,
  onTypingFinished,
  keyDelayMs = DEFAULT_KEY_DELAY_MS,
}: VncClipboardPanelProps) {
  const textId = useId()
  const layoutId = useId()
  const guestId = useId()

  const [text, setText] = useState("")
  const [layout, setLayout] = useState<KeyboardLayout>(storedLayout)
  const [progress, setProgress] = useState<{ typed: number; total: number } | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const abortRef = useRef<AbortController | null>(null)
  const senderRef = useRef(sender)
  useEffect(() => {
    senderRef.current = sender
  }, [sender])

  // Cancel an in-flight paste if the panel goes away mid-typing, otherwise the
  // loop keeps pushing keys at a console the user has already left.
  useEffect(() => () => abortRef.current?.abort(), [])

  const plan = useMemo(() => planKeystrokes(text, layout), [text, layout])
  const typing = progress !== null
  const canSend = sender !== null && plan.keystrokes.length > 0 && !typing

  const selectLayout = (next: KeyboardLayout) => {
    setLayout(next)
    try {
      window.localStorage.setItem(LAYOUT_STORAGE_KEY, next)
    } catch {
      // ignore storage quota / private-mode failures
    }
  }

  const readBrowserClipboard = async () => {
    setNotice(null)
    try {
      setText(await navigator.clipboard.readText())
    } catch {
      setNotice("Could not read the browser clipboard. Paste into the box instead.")
    }
  }

  const copyGuestClipboard = async () => {
    if (guestClipboard === null) return
    try {
      await navigator.clipboard.writeText(guestClipboard)
      setNotice("Copied to the browser clipboard.")
    } catch {
      setNotice("Could not write to the browser clipboard. Select the text and copy it.")
    }
  }

  const send = async () => {
    const target = sender
    if (!target) return

    const controller = new AbortController()
    abortRef.current = controller
    setNotice(null)
    setProgress({ typed: 0, total: plan.keystrokes.length })

    const result = await typeKeystrokes(target, plan.keystrokes, {
      delayMs: keyDelayMs,
      signal: controller.signal,
      isConnected: () => senderRef.current !== null,
      onProgress: (typed, total) => setProgress({ typed, total }),
    })

    abortRef.current = null
    setProgress(null)
    setNotice(
      result.stopped === "done"
        ? `Typed ${result.typed} characters.`
        : result.stopped === "aborted"
          ? `Cancelled after ${result.typed} characters.`
          : `Session dropped after ${result.typed} characters.`,
    )
    onTypingFinished?.()
  }

  return (
    <div className="shrink-0 border-t border-slate-800 bg-[#171b26] px-3 py-3 text-slate-300">
      <div className="flex items-center justify-between">
        <span className="text-[11px] font-medium tracking-widest text-slate-500 uppercase">
          Clipboard
        </span>
        <button
          type="button"
          onClick={onClose}
          title="Close clipboard panel"
          className="cursor-pointer rounded p-1 text-slate-500 transition-colors hover:bg-slate-700/60 hover:text-slate-200"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <div className="mt-2 grid gap-3 md:grid-cols-2">
        {/* ── Browser → VM ── */}
        <div className="flex flex-col gap-2">
          <label htmlFor={textId} className="text-[11px] text-slate-500">
            Text to type into the VM
          </label>
          <textarea
            id={textId}
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={4}
            spellCheck={false}
            className="w-full resize-y rounded-md border border-slate-700 bg-[#0d0f14] p-2 font-mono text-xs text-slate-200 outline-none focus:border-slate-500"
          />

          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor={layoutId} className="text-[11px] text-slate-500">
              Guest keyboard layout
            </label>
            <select
              id={layoutId}
              value={layout}
              onChange={(e) => selectLayout(e.target.value as KeyboardLayout)}
              className="cursor-pointer rounded border border-slate-700 bg-[#0d0f14] px-1.5 py-1 text-[11px] text-slate-300 outline-none focus:border-slate-500"
            >
              {KEYBOARD_LAYOUTS.map((l) => (
                <option key={l} value={l}>
                  {LAYOUT_LABELS[l]}
                </option>
              ))}
            </select>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <PanelButton onClick={readBrowserClipboard} disabled={typing}>
              <ClipboardPaste className="h-3 w-3" /> Read clipboard
            </PanelButton>

            {typing ? (
              <>
                <PanelButton onClick={() => abortRef.current?.abort()}>Cancel</PanelButton>
                <span className="text-[11px] text-slate-500">
                  {progress.typed} / {progress.total}
                </span>
              </>
            ) : (
              <PanelButton onClick={send} disabled={!canSend} primary>
                <Send className="h-3 w-3" /> Send to VM
              </PanelButton>
            )}
          </div>

          {plan.unsupported.length > 0 && (
            <p className="text-[11px] text-amber-400">
              {plan.unsupported.length} character
              {plan.unsupported.length === 1 ? "" : "s"} cannot be typed on the{" "}
              {LAYOUT_LABELS[layout]} layout and will be skipped:{" "}
              <span className="font-mono">
                {plan.unsupported.slice(0, 12).map(describe).join(" ")}
              </span>
              {plan.unsupported.length > 12 ? " …" : ""}
            </p>
          )}
        </div>

        {/* ── VM → browser ── */}
        <div className="flex flex-col gap-2">
          <label htmlFor={guestId} className="text-[11px] text-slate-500">
            Text pushed by the VM
          </label>
          {guestClipboard === null ? (
            <p className="text-[11px] text-slate-500">
              Nothing received from the VM. A guest can only push its clipboard over VNC when the
              hypervisor attaches a clipboard agent to the console, which KubeVirt does not do
              today — so this stays empty on a stock cluster.
            </p>
          ) : (
            <>
              <textarea
                id={guestId}
                value={guestClipboard}
                readOnly
                rows={4}
                className="w-full resize-y rounded-md border border-slate-700 bg-[#0d0f14] p-2 font-mono text-xs text-slate-200 outline-none"
              />
              <div>
                <PanelButton onClick={copyGuestClipboard}>
                  <Copy className="h-3 w-3" /> Copy to browser
                </PanelButton>
              </div>
            </>
          )}
        </div>
      </div>

      {notice && <p className="mt-2 text-[11px] text-slate-400">{notice}</p>}
    </div>
  )
}

function PanelButton({
  onClick,
  disabled,
  primary,
  children,
}: {
  onClick: () => void
  disabled?: boolean
  primary?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`flex cursor-pointer items-center gap-1.5 rounded-md px-2.5 py-1.5 text-[11px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40 ${
        primary
          ? "bg-slate-200 text-slate-900 hover:bg-white"
          : "bg-slate-800 text-slate-300 ring-1 ring-slate-700 hover:bg-slate-700 hover:text-white"
      }`}
    >
      {children}
    </button>
  )
}
