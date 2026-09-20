export type ThemePreference = 'system' | 'light' | 'dark'

const STORAGE_KEY = 'shipwick-theme'

function readStored(): ThemePreference {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    return stored === 'light' || stored === 'dark' ? stored : 'system'
  }
  catch {
    return 'system'
  }
}

/**
 * Theme preference. "system" means no data-theme attribute at all, so the CSS
 * follows prefers-color-scheme by itself, live. The stored choice is applied
 * before first paint by the inline script in nuxt.config.ts.
 */
export function useTheme() {
  const preference = useState<ThemePreference>('theme:preference', () => (import.meta.client ? readStored() : 'system'))

  function set(next: ThemePreference) {
    preference.value = next
    const root = document.documentElement
    if (next === 'system') root.removeAttribute('data-theme')
    else root.setAttribute('data-theme', next)
    try {
      if (next === 'system') localStorage.removeItem(STORAGE_KEY)
      else localStorage.setItem(STORAGE_KEY, next)
    }
    catch {
      // Storage unavailable (private mode): the choice lasts for this page only.
    }
  }

  return { preference, set }
}
