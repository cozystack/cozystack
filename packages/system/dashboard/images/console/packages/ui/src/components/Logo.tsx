import type { CSSProperties } from "react"
import logoUrl from "../assets/logo.svg"

interface LogoProps {
  className?: string
  title?: string
  /** Base64-encoded SVG to use instead of the default logo. */
  svgContent?: string
  /** Plain text label to render when no SVG is provided. */
  text?: string
  /** Inline styles, used by the header to set an explicit logo height. */
  style?: CSSProperties
}

export function Logo({ className, title = "Cozystack", svgContent, text, style }: LogoProps) {
  if (svgContent) {
    return (
      <img
        src={`data:image/svg+xml;base64,${svgContent}`}
        alt={title}
        className={className}
        style={style}
      />
    )
  }
  if (text) {
    return (
      <span className={className} style={style}>
        {text}
      </span>
    )
  }
  return <img src={logoUrl} alt={title} className={className} style={style} />
}
