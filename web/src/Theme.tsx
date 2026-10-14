import { createContext, useContext, useEffect, useMemo, useState } from 'react'

/**
 * Theme choice, held on the document and remembered.
 *
 * # Why this is three lines of state and no component changes
 *
 * Every colour utility in the interface compiles to a CSS variable, so a theme is a set of variable values under a
 * data-theme selector. Switching one attribute on the root element retheme fourteen hundred utilities across sixty files.
 * Nothing else has to know, which is the only reason offering three themes is a reasonable thing to do rather than a
 * rewrite.
 *
 * # Why it is stored per browser rather than per account
 *
 * A theme is a property of where somebody is sitting, not of who they are. The same reviewer may want midnight at a desk in a dim
 * office and light on a laptop by the window, and an account-level setting would fight them. It also
 * means the choice survives with no server round trip and works before sign-in.
 *
 * # Why the system preference is only a default
 *
 * Following prefers-color-scheme forever would override a deliberate choice the moment the operating system changed at
 * sunset. It is consulted once, when nobody has chosen yet, and then the person's own choice wins for good.
 */
export const THEMES = ['midnight', 'dark', 'light'] as const

export type Theme = (typeof THEMES)[number]

/** How each theme is described where somebody picks one. The label alone does not say when to use it. */
export const THEME_LABELS: Record<Theme, { label: string; about: string }> = {
  midnight: {
    label: 'Midnight',
    about: 'Near-black and high contrast. For a dim room and a long queue.',
  },
  dark: {
    label: 'Dark',
    about: 'Neutral grey, softer at the extremes. Easier for a long day at a desk.',
  },
  light: {
    label: 'Light',
    about: 'For bright rooms, printing, and sharing a screen with somebody.',
  },
}

const STORAGE_KEY = 'vaglio.theme'

function isTheme(v: unknown): v is Theme {
  return typeof v === 'string' && (THEMES as readonly string[]).includes(v)
}

/** The theme to start with: a previous choice, else the system preference, else midnight. */
export function initialTheme(): Theme {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY)
    if (isTheme(saved)) return saved
  } catch {
    // Private browsing and some managed configurations refuse localStorage. A theme is not worth an error, and the
    // default below is a perfectly good answer.
  }

  try {
    if (window.matchMedia('(prefers-color-scheme: light)').matches) return 'light'
  } catch {
    // Equally optional.
  }

  return 'midnight'
}

type Ctx = { theme: Theme; setTheme: (t: Theme) => void }

const ThemeContext = createContext<Ctx>({ theme: 'midnight', setTheme: () => {} })

export function useTheme() {
  return useContext(ThemeContext)
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initialTheme)

  useEffect(() => {
    // The attribute goes on the root element, which is what the CSS selects on. Set here rather than in index.html so that
    // one place owns it - two writers would race on first paint.
    document.documentElement.dataset.theme = theme

    try {
      window.localStorage.setItem(STORAGE_KEY, theme)
    } catch {
      // See initialTheme. Refusing to switch theme because it cannot be remembered would be the wrong trade.
    }
  }, [theme])

  const value = useMemo(() => ({ theme, setTheme }), [theme])

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}
