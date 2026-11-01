// Preferencias de la app de cada persona (se guardan en la cuenta): idioma al
// buscar libros, fuente de cómics, visibilidad al añadir y tema. Aquí queda
// una copia para arrancar sin esperar a la red.
import { api } from './util.js'

export const defaultPrefs = { search_lang: 'es', comic_source: 'todas', visibility: 'privado', theme: 'system' }

let current = load()

function load() {
  try { return { ...defaultPrefs, ...JSON.parse(localStorage.getItem('libros.prefs') ?? '{}') } } catch { return { ...defaultPrefs } }
}

function keep() {
  try { localStorage.setItem('libros.prefs', JSON.stringify(current)) } catch {}
}

export const prefs = () => current

export function applyTheme(theme = current.theme) {
  const root = document.documentElement
  if (theme === 'light' || theme === 'dark') root.dataset.theme = theme
  else delete root.dataset.theme
  try {
    if (theme === 'light' || theme === 'dark') localStorage.setItem('libros.theme', theme)
    else localStorage.removeItem('libros.theme')
  } catch {}
  const bg = getComputedStyle(document.body).getPropertyValue('background-color')
  for (const m of document.querySelectorAll('meta[name=theme-color]')) m.setAttribute('content', bg)
}

// fromServer toma las preferencias que vienen con los ajustes de la cuenta.
export function fromServer(p) {
  if (!p) return
  current = { ...defaultPrefs, ...p }
  keep()
  applyTheme()
}

export async function savePrefs(patch) {
  current = { ...current, ...patch }
  keep()
  applyTheme()
  const doc = await api('api/prefs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(current) })
  current = { ...defaultPrefs, ...doc.prefs }
  keep()
  return current
}
