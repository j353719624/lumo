// Theme bootstrap: an explicit choice wins, otherwise follow the OS setting.
export type Theme = 'dark' | 'light'
const KEY = 'lumo-theme'

export function initTheme(): Theme {
  let saved: string | null = null
  try { saved = localStorage.getItem(KEY) } catch { /* private mode */ }
  const theme: Theme =
    saved === 'light' || saved === 'dark'
      ? saved
      : window.matchMedia?.('(prefers-color-scheme: light)').matches
        ? 'light'
        : 'dark'
  document.documentElement.dataset.theme = theme
  return theme
}

export function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t
  try { localStorage.setItem(KEY, t) } catch { /* ignore */ }
}
