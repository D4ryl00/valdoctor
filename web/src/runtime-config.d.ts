export {}

declare global {
  interface Window {
    __VALDOCTOR_CONFIG__?: {
      apiBaseURL?: string
    }
  }
}
