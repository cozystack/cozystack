import { useState, useCallback, useEffect, useRef, useMemo } from "react"
import type { CommandItem } from "./types"

export function useKeyboardNav(items: CommandItem[]) {
  const [highlightedIndex, setHighlightedIndex] = useState(0)
  const itemRefs = useRef<Map<number, HTMLElement>>(new Map())

  const itemsKey = useMemo(() => items.map((i) => i.id).join(","), [items])

  useEffect(() => {
    setHighlightedIndex(0)
  }, [itemsKey])

  useEffect(() => {
    const el = itemRefs.current.get(highlightedIndex)
    el?.scrollIntoView({ block: "nearest" })
  }, [highlightedIndex])

  const setItemRef = useCallback((index: number, el: HTMLElement | null) => {
    if (el) {
      itemRefs.current.set(index, el)
    } else {
      itemRefs.current.delete(index)
    }
  }, [])

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (items.length === 0) return

      switch (e.key) {
        case "ArrowDown": {
          e.preventDefault()
          setHighlightedIndex((i) => (i + 1) % items.length)
          break
        }
        case "ArrowUp": {
          e.preventDefault()
          setHighlightedIndex((i) => (i - 1 + items.length) % items.length)
          break
        }
        case "Home": {
          e.preventDefault()
          setHighlightedIndex(0)
          break
        }
        case "End": {
          e.preventDefault()
          setHighlightedIndex(items.length - 1)
          break
        }
        case "Enter": {
          e.preventDefault()
          items[highlightedIndex]?.onSelect()
          break
        }
      }
    },
    [items, highlightedIndex]
  )

  return { highlightedIndex, onKeyDown, setItemRef }
}
