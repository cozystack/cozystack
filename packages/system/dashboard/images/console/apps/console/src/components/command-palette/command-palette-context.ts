import { createContext, useContext } from "react"

export interface CommandPaletteContextValue {
  open: boolean
  setOpen: (open: boolean) => void
  toggle: () => void
}

// Kept out of command-palette-provider.tsx: a file exporting a component
// alongside a context or a hook breaks Fast Refresh.
export const CommandPaletteContext = createContext<CommandPaletteContextValue | undefined>(
  undefined
)

export function useCommandPalette() {
  const context = useContext(CommandPaletteContext)
  if (!context) {
    throw new Error("useCommandPalette must be used within CommandPaletteProvider")
  }
  return context
}
