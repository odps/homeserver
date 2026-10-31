// Arranque: rutas por hash (#/ biblioteca, #/leer/<id> lector), service
// worker para el modo sin conexión y envío del progreso pendiente.
import { $, api, banner, me, toast, Offline } from './util.js'
import { flushQueue, pendingCount } from './local.js'
import { showLibrary } from './library.js'

const view = $('#view')

async function route() {
  const m = location.hash.match(/^#\/leer\/(\d+)/)
  $('#dialog').open && $('#dialog').close()
  if (m) {
    const { showReader } = await import('./reader.js')
    await showReader(view, Number(m[1]))
  } else {
    const { closeReader } = await import('./reader.js')
    closeReader()
    document.querySelector('meta[name=theme-color]')?.setAttribute('content', getComputedStyle(document.body).backgroundColor)
    await showLibrary(view)
  }
}

async function connectivity() {
  if (navigator.onLine) {
    await flushQueue()
    banner(null)
  } else {
    const n = await pendingCount()
    banner(`Sin conexión. ${n ? `${n} ${n === 1 ? 'libro tiene' : 'libros tienen'} progreso pendiente de sincronizar.` : 'Puedes leer los libros guardados en este dispositivo.'}`)
  }
}

window.addEventListener('hashchange', route)
window.addEventListener('online', () => { connectivity(); toast('Conexión recuperada: progreso sincronizado') })
window.addEventListener('offline', connectivity)
// Reintento periódico por si la red vuelve sin avisar (redes móviles).
setInterval(() => navigator.onLine && flushQueue(), 60_000)

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('sw.js').catch(e => console.warn('service worker', e))
}

// Quién soy: de la red o, sin conexión, de la última vez.
try {
  Object.assign(me, await api('api/me'))
} catch (e) {
  if (e instanceof Offline) {
    try { Object.assign(me, JSON.parse(localStorage.getItem('libros.me')) ?? {}) } catch {}
  }
}

// Si en este navegador entra otra persona, se borran los libros guardados y
// las copias de la anterior: los privados no deben quedar a su alcance.
if (me.user) {
  const last = localStorage.getItem('libros.lastUser')
  if (last && last !== me.user && 'caches' in window) {
    for (const name of ['libros-books', 'libros-data', 'libros-covers']) await caches.delete(name)
    localStorage.removeItem(`libros.library.${last}`)
  }
  localStorage.setItem('libros.lastUser', me.user)
}

connectivity()
route()
