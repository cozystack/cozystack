import { useState } from "react"
import { Eye, EyeOff } from "lucide-react"
import type { WidgetProps } from "@rjsf/utils"

// Matches the styling applied to `input[type="text"]` in schema-form.css.
// The CSS rule there is keyed on the `type` attribute, so a `type="password"`
// input would otherwise fall back to native browser chrome and visibly differ
// from neighbouring fields every time the user toggles reveal.
const INPUT_CLASS =
  "w-full rounded-md border border-slate-200 bg-white px-2.5 py-1.5 text-sm text-slate-900 shadow-sm outline-none transition-shadow focus:border-blue-500 focus:ring-2 focus:ring-blue-500/20 disabled:opacity-50 disabled:cursor-not-allowed"

const TOGGLE_CLASS =
  "flex size-8 shrink-0 items-center justify-center rounded-md text-slate-500 hover:bg-slate-100"

export function SensitiveStringWidget(props: WidgetProps) {
  const { id, value, onChange, required, disabled, readonly, autofocus, placeholder } = props
  const [revealed, setRevealed] = useState(false)

  const stringValue = typeof value === "string" ? value : ""

  return (
    <div className="flex items-center gap-2">
      <input
        id={id}
        type={revealed ? "text" : "password"}
        // Best-effort hint to skip browser password-manager autofill on this
        // ad-hoc credential field. Browser behaviour varies; `new-password` is
        // the most reliable value but the prompt is not fully suppressible.
        autoComplete="new-password"
        autoFocus={autofocus}
        placeholder={placeholder}
        value={stringValue}
        required={required}
        disabled={disabled}
        readOnly={readonly}
        onChange={(event) => {
          // Empty string is coerced to undefined so the surrounding form
          // drops the key from the spec entirely on clear, matching the
          // convention used by the other custom widgets in this folder.
          const next = event.target.value
          onChange(next === "" ? undefined : next)
        }}
        className={INPUT_CLASS}
      />
      <button
        type="button"
        onClick={() => setRevealed((prev) => !prev)}
        aria-label="Toggle credential visibility"
        aria-pressed={revealed}
        className={TOGGLE_CLASS}
      >
        {revealed ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  )
}
