declare module '@novnc/novnc' {
  export default class RFB {
    constructor(target: HTMLElement, url: string, options?: Record<string, unknown>)
    scaleViewport: boolean
    resizeSession: boolean
    connect(): void
    disconnect(): void
    sendCredentials(credentials: { password?: string; username?: string }): void
    addEventListener(type: string, listener: (e?: any) => void): void
    removeEventListener(type: string, listener: (e?: any) => void): void
  }
}
