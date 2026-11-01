// Ajustes de lectura de la cuenta: se guardan en el servidor y se aplican a
// todos los libros en cualquier dispositivo. Aquí queda una copia para abrir
// sin conexión; un cambio hecho sin red se envía al volver y gana el más
// reciente.
import { api, me } from './util.js'
import { fromServer } from './prefs.js'

export const defaults = { theme: 'light', size: 100, spacing: 1.5, margin: 'normal', justify: true, flow: 'paginated', font: 'original' }

const key = () => `libros.reader.${me.user}`

function local() {
  try {
    const v = JSON.parse(localStorage.getItem(key()) ?? 'null')
    if (v) return v
    // Ajustes de antes de guardarse en la cuenta (de este dispositivo).
    const old = JSON.parse(localStorage.getItem('libros.reader') ?? 'null')
    return old ? { reader: old, updated_at: new Date(0).toISOString(), dirty: true } : null
  } catch { return null }
}

function keep(v) {
  try { localStorage.setItem(key(), JSON.stringify(v)) } catch {}
}

export const readerSettings = () => ({ ...defaults, ...(local()?.reader ?? {}) })

let timer
export function saveReaderSettings(s) {
  const v = { reader: { ...s }, updated_at: new Date().toISOString(), dirty: true }
  keep(v)
  clearTimeout(timer)
  timer = setTimeout(() => push(v), 800)
}

async function push(v) {
  try {
    const srv = await api('api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reader: { ...defaults, ...v.reader }, updated_at: v.updated_at }) })
    // Solo si no ha habido otro cambio mientras tanto.
    if (local()?.updated_at === v.updated_at) keep({ reader: srv.reader, updated_at: srv.updated_at, dirty: false })
  } catch {} // sin red: queda pendiente y se envía en la próxima sincronización
}

// syncSettings trae los ajustes de la cuenta, o envía los de aquí si son un
// cambio pendiente más reciente.
export async function syncSettings() {
  if (!me.user) return
  const l = local()
  try {
    const srv = await api('api/settings')
    fromServer(srv.prefs)
    if (l?.dirty && (!srv.updated_at || new Date(l.updated_at) > new Date(srv.updated_at))) await push(l)
    else if (srv.updated_at) keep({ reader: srv.reader, updated_at: srv.updated_at, dirty: false })
    if (local()) localStorage.removeItem('libros.reader')
  } catch {}
}
