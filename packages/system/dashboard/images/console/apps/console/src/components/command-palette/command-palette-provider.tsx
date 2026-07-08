import { createContext, useContext, useState, useCallback, useMemo } from "react"
import { useHotkey } from "./use-hotkey"

interface CommandPaletteContextValue {
  open: boolean
  setOpen: (open: boolean) => void
  toggle: () => void
}

const CommandPaletteContext = createContext<CommandPaletteContextValue | undefined>(
  undefined
)

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

export function useCommandPalette() {
  const context = useContext(CommandPaletteContext)
  if (!context) {
    throw new Error("useCommandPalette must be used within CommandPaletteProvider")
  }
  return context
}
