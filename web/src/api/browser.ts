import { apiFetch } from './client'

export interface BrowserPage {
  url: string
  title: string
  text: string
  html?: string
}

export function fetchBrowserPage(url: string): Promise<{ data: BrowserPage }> {
  return apiFetch(`/browser/fetch?url=${encodeURIComponent(url)}`)
}
