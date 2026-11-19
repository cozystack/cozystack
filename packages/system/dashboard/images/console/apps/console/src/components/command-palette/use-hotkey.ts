import { useEffect, useCallback } from "react"

function isEditableElement(el: EventTarget | null): boolean {
  if (!el || !(el instanceof HTMLElement)) return false
  const tag = el.tagName
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true
  if (el.isContentEditable) return true
  if (el.closest("[data-slot='input']")) return true
  return false
}

export function useHotkey(onToggle: () => void) {
  const handler = useCallback(
    (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault()
        onToggle()
        return
      }

      if (e.key === "/" && !isEditableElement(e.target)) {
        e.preventDefault()
        onToggle()
      }
    },
    [onToggle]
  )

  useEffect(() => {
    document.addEventListener("keydown", handler)
    return () => document.removeEventListener("keydown", handler)
  }, [handler])
}
