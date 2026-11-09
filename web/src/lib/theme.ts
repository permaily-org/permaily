export type Theme = 'light' | 'dark' | 'system'

const storageKey = 'permaily-theme'

export function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(storageKey)
    return stored === 'light' || stored === 'dark' ? stored : 'system'
  } catch {
    return 'system'
  }
}

/** Sets data-theme on <html>, or removes it so colors follow the system setting. */
export function applyTheme(theme: Theme): void {
  const root = document.documentElement
  if (theme === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', theme)
}

export function saveTheme(theme: Theme): void {
  try {
    if (theme === 'system') localStorage.removeItem(storageKey)
    else localStorage.setItem(storageKey, theme)
  } catch {
    // Storage can be unavailable, for example in a private window. The choice then lasts for this visit.
  }
}
