import { useState, useCallback, useMemo } from "react"
import { useHotkey } from "./use-hotkey"
import { CommandPaletteContext } from "./command-palette-context.ts"

export function CommandPaletteProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false)

  const toggle = useCallback(() => {
    setOpen((prev) => !prev)
  }, [])

  useHotkey(toggle)

  const value = useMemo(
    () => ({ open, setOpen, toggle }),
    [open, toggle]
  )

  return (
    <CommandPaletteContext.Provider value={value}>
      {children}
    </CommandPaletteContext.Provider>
  )
}
