// Arranque y navegación. Rutas por hash:
//   #/               inicio (continuar leyendo, capítulos nuevos, favoritos)
//   #/libros[/id]    biblioteca por estados y ficha de un libro
//   #/leer/id        lector
//   #/comics[/id]    series que sigues y ficha de una serie
//   #/descubrir/...  bibliotecas públicas y buscador de series
//   #/perfil         cuenta, preferencias y ajustes de lectura
// Al volver atrás se recupera la posición del scroll de la página anterior.
// install.js primero: el aviso de instalación puede llegar muy pronto.
import './install.js'
import { $, h, icon, api, banner, me, toast, Offline } from './util.js'
import { flushQueue, pendingCount } from './local.js'
import { syncSettings } from './settings.js'
import { applyTheme } from './prefs.js'

const view = $('#view')

// --- Navegación ---

const NAV = [
  ['inicio', 'home', 'Inicio'],
  ['libros', 'book', 'Libros'],
  ['comics', 'layers', 'Cómics'],
  ['descubrir', 'compass', 'Descubrir'],
  ['perfil', null, 'Perfil'],
]

function renderNav() {
  for (const [id, ic, label] of NAV) {
    const a = document.querySelector(`.nav [data-nav=${id}]`)
    const lead = ic ? icon(ic) : h('span', { class: 'avatar', 'aria-hidden': 'true' }, (me.name || me.user || '?').trim()[0]?.toUpperCase() ?? '?')
    a.replaceChildren(lead, h('span', {}, id === 'perfil' && me.name ? me.name.split(' ')[0] : label))
    if (id === 'comics') a.append(h('b', { class: 'badge', hidden: true, id: 'nav-unread' }))
    a.setAttribute('aria-label', label)
  }
}

function markNav(section) {
  for (const a of document.querySelectorAll('.nav [data-nav]')) {
    if (a.dataset.nav === section) a.setAttribute('aria-current', 'page')
    else a.removeAttribute('aria-current')
  }
}

// Capítulos nuevos sin leer en las series empezadas, en el botón Cómics.
export async function refreshUnread(list) {
  try {
    list ??= await api('api/series')
    const n = list.filter(s => s.last_read != null).reduce((t, s) => t + s.unread, 0)
    const b = $('#nav-unread')
    if (!b) return
    b.textContent = n > 99 ? '99+' : String(n)
    b.hidden = !n
    b.title = n === 1 ? '1 capítulo nuevo' : `${n} capítulos nuevos`
  } catch {}
}
window.addEventListener('libros:series', e => refreshUnread(e.detail))

// --- Rutas ---

const legacy = [
  [/^#\/explorar/, '#/descubrir/libros'],
  [/^#\/series\/buscar/, '#/descubrir/comics'],
  [/^#\/series\/(\d+)/, '#/comics/$1'],
  [/^#\/series/, '#/comics'],
]

function parse(hash) {
  for (const [re, to] of legacy) if (re.test(hash)) return parse(hash.replace(re, to))
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  return { section: parts[0] || 'inicio', id: parts[1], hash }
}

const modules = {
  inicio: () => import('./home.js'),
  libros: () => import('./books.js'),
  leer: () => import('./reader.js'),
  comics: () => import('./comics.js'),
  descubrir: () => import('./discover.js'),
  perfil: () => import('./profile.js'),
}

// Historial propio para saber si se vuelve atrás (y recuperar el scroll).
const stack = [location.hash || '#/']
const scrolls = new Map()
let currentHash = location.hash || '#/'

async function route({ back = false } = {}) {
  const r = parse(location.hash || '#/')
  if (r.hash !== (location.hash || '#/') && r.hash !== location.hash) {
    history.replaceState(null, '', r.hash)
  }
  $('#dialog').open && $('#dialog').close()
  const reading = r.section === 'leer'
  document.body.classList.toggle('reading', reading)
  if (!reading) {
    const { closeReader } = await import('./reader.js')
    closeReader()
    applyTheme()
  }
  markNav(r.section)
  const load = modules[r.section] ?? modules.inicio
  const mod = await load()
  // La página se pinta siempre una vez; la transición solo la acompaña (el
  // navegador puede saltársela, p. ej. si llega otra navegación).
  let rendering
  const render = () => (rendering ??= Promise.resolve(reading ? mod.showReader(view, Number(r.id)) : mod.show(view, r, { back })).catch(e => {
    console.error(e)
    toast(e.message)
  }))
  const sleep = ms => new Promise(res => setTimeout(res, ms))
  if (document.startViewTransition && !reading && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    const t = document.startViewTransition(() => Promise.race([render(), sleep(300)]))
    for (const p of [t.ready, t.finished, t.updateCallbackDone]) p.catch(() => {})
    await Promise.race([t.updateCallbackDone.catch(() => {}), sleep(400)])
  }
  await Promise.race([render(), sleep(300)])
  // La página puede seguir cargando: se espera un poco a que tenga altura.
  const y = back ? scrolls.get(r.hash) ?? 0 : 0
  const restore = (tries = 0) => {
    if (document.documentElement.scrollHeight - innerHeight >= y || tries > 20) scrollTo(0, y)
    else setTimeout(() => restore(tries + 1), 50)
  }
  restore()
}

window.addEventListener('hashchange', () => {
  scrolls.set(currentHash, scrollY)
  const next = location.hash || '#/'
  const back = stack.length > 1 && stack[stack.length - 2] === next
  if (back) stack.pop()
  else stack.push(next)
  if (stack.length > 50) stack.shift()
  currentHash = next
  route({ back })
})

// --- Conexión y progreso pendiente ---

// serverDown: el móvil tiene red pero el servidor no contesta (cobertura a
// medias, túnel caído). Se avisa y no se quita hasta que vuelva a contestar.
let serverDown = false
window.addEventListener('libros:sinservidor', () => {
  if (serverDown || !navigator.onLine) return
  serverDown = true
  banner('Sin conexión con el servidor: puedes leer los libros guardados en este dispositivo.')
})
window.addEventListener('libros:conservidor', () => {
  if (!serverDown) return
  serverDown = false
  banner(null)
})

async function connectivity() {
  if (navigator.onLine) {
    await Promise.all([flushQueue(), syncSettings()])
    if (!serverDown) banner(null)
  } else {
    const n = await pendingCount()
    banner(`Sin conexión. ${n ? `${n} ${n === 1 ? 'libro tiene' : 'libros tienen'} progreso pendiente de sincronizar.` : 'Puedes leer los libros guardados en este dispositivo.'}`)
  }
}

window.addEventListener('online', () => { connectivity(); toast('Conexión recuperada: progreso sincronizado') })
window.addEventListener('offline', connectivity)
// Reintento periódico por si la red vuelve sin avisar (redes móviles).
setInterval(() => navigator.onLine && flushQueue(), 60_000)

if ('serviceWorker' in navigator) {
  const hadController = !!navigator.serviceWorker.controller
  navigator.serviceWorker.register('sw.js').then(reg => {
    // Busca versiones nuevas al abrir y cada hora.
    reg.update().catch(() => {})
    setInterval(() => reg.update().catch(() => {}), 3_600_000)
  }).catch(e => console.warn('service worker', e))
  // Versión nueva activada: se recarga para usarla, salvo a mitad de lectura.
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (!hadController) return
    if (location.hash.startsWith('#/leer/')) toast('Hay una versión nueva: se aplicará al salir del lector')
    else location.reload()
  })
}

// Quién soy: si ya entró antes en este dispositivo, se arranca con lo que se
// recuerda y se comprueba en segundo plano (así abre sin esperar a la red);
// la primera vez, de la red.
let remembered = null
try { remembered = JSON.parse(localStorage.getItem('libros.me')) } catch {}
if (remembered?.user) {
  Object.assign(me, remembered)
  api('api/me').then(fresh => {
    Object.assign(me, fresh)
    try { localStorage.setItem('libros.me', JSON.stringify(me)) } catch {}
    window.dispatchEvent(new Event('libros:me'))
  }).catch(e => {
    if (e instanceof Offline && navigator.onLine) window.dispatchEvent(new Event('libros:sinservidor'))
  })
} else {
  try {
    Object.assign(me, await api('api/me'))
    try { localStorage.setItem('libros.me', JSON.stringify(me)) } catch {}
  } catch {}
}

// Si en este navegador entra otra persona, se borran los libros guardados y
// las copias de la anterior: los privados no deben quedar a su alcance.
if (me.user) {
  const last = localStorage.getItem('libros.lastUser')
  if (last && last !== me.user && 'caches' in window) {
    for (const name of ['libros-books', 'libros-data', 'libros-covers']) await caches.delete(name)
    localStorage.removeItem(`libros.library.${last}`)
    localStorage.removeItem('libros.prefs')
  }
  localStorage.setItem('libros.lastUser', me.user)
}

applyTheme()
renderNav()
window.addEventListener('libros:me', renderNav)
connectivity()
route()
refreshUnread()
