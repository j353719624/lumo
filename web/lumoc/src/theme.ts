// Theme bootstrap: 已选的偏好优先；默认日间模式（浅色）。
// 想跟随系统或固定深色，可在侧栏的主题按钮里切换。
export type Theme = 'dark' | 'light'
export type ThemePref = Theme | 'auto'
const KEY = 'lumo-theme'

function systemTheme(): Theme {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

export function getThemePref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark' || v === 'auto') return v
  } catch { /* private mode */ }
  return 'light'
}

export function applyThemePref(pref: ThemePref): Theme {
  const theme: Theme = pref === 'auto' ? systemTheme() : pref
  document.documentElement.dataset.theme = theme
  try { localStorage.setItem(KEY, pref) } catch { /* ignore */ }
  return theme
}

export function initTheme(): Theme {
  return applyThemePref(getThemePref())
}

// 跟随系统时，系统切换后自动跟随
if (window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: light)')
  mq.addEventListener?.('change', () => {
    if (getThemePref() === 'auto') applyThemePref('auto')
  })
}
