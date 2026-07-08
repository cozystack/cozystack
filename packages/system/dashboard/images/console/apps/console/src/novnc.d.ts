declare module '@novnc/novnc/lib/rfb' {
  export default class RFB {
    constructor(
      target: HTMLElement,
      url: string,
      options?: {
        credentials?: { username?: string; password?: string; target?: string }
        repeaterID?: string
        shared?: boolean
      }
    )
    disconnect(): void
    sendCredentials(creds: { username?: string; password?: string; target?: string }): void
    sendKey(keysym: number, code: string, down?: boolean): void
    sendCtrlAltDel(): void
    focus(): void
    blur(): void
    clipboardPasteFrom(text: string): void
    scaleViewport: boolean
    resizeSession: boolean
    showDotCursor: boolean
    background: string
    qualityLevel: number
    compressionLevel: number
    viewOnly: boolean
    focusOnClick: boolean
    clipViewport: boolean
    dragViewport: boolean
    addEventListener(type: string, listener: EventListener): void
    removeEventListener(type: string, listener: EventListener): void
  }
}
