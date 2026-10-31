// Arranque: rutas por hash (#/ biblioteca, #/leer/<id> lector), service
// worker para el modo sin conexión y envío del progreso pendiente.
import { $, banner, toast } from './util.js'
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

connectivity()
route()
