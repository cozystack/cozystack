import { useState, useEffect } from "react"

export function useIsMac(): boolean {
  const [isMac, setIsMac] = useState(false)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- a lazy initial state would change what the first render returns
    setIsMac(navigator.platform.toUpperCase().indexOf("MAC") >= 0)
  }, [])

  return isMac
}
