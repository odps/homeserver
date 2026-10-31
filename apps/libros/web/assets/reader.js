// Lector: foliate-js a pantalla completa con barras que aparecen al tocar el
// centro, zonas laterales para pasar página, índice y ajustes de lectura.
// Guarda la posición en cada cambio de página (con un pequeño retardo) y, al
// abrir, continúa desde la posición más reciente de cualquier dispositivo.
import { h, icon, api, pct, toast, Offline } from './util.js'
import { cachedLibrary, loadFile, localProgress, saveProgress } from './local.js'

const defaults = { theme: 'light', size: 100, spacing: 1.5, margin: 'normal', justify: true, flow: 'paginated', font: 'original' }
const settings = () => ({ ...defaults, ...JSON.parse(localStorage.getItem('libros.reader') ?? '{}') })
const saveSettings = s => localStorage.setItem('libros.reader', JSON.stringify(s))

const themes = { light: ['#ffffff', '#1d1c1a', '#2f6f5e'], sepia: ['#f4ecd8', '#5b4636', '#8a5a2b'], dark: ['#121212', '#d7d4cd', '#6cc3a7'] }
const margins = { narrow: ['24px', '3%'], normal: ['48px', '6%'], wide: ['64px', '10%'] }
const fonts = { original: '', serif: 'Georgia, "Iowan Old Style", "Palatino Linotype", serif', sans: 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif' }

// Estilos que se inyectan en el libro. En los temas sepia y oscuro se fuerzan
// los colores, porque muchos libros fijan texto negro sobre blanco.
function css(s) {
  const [bg, fg, link] = themes[s.theme]
  const force = s.theme !== 'light'
  return `
    html { color-scheme: ${s.theme === 'dark' ? 'dark' : 'light'}; ${force ? `color: ${fg} !important; background: ${bg} !important;` : ''} }
    html { font-size: ${s.size}% !important; }
    ${force ? `body, p, div, span, li, h1, h2, h3, h4, h5, h6, td, th, blockquote { color: inherit !important; background-color: transparent !important; }` : ''}
    a:link, a:visited { color: ${link} !important; }
    ${fonts[s.font] ? `body, p, div, span, li, blockquote { font-family: ${fonts[s.font]} !important; }` : ''}
    p, li, blockquote, dd {
      line-height: ${s.spacing} !important;
      text-align: ${s.justify ? 'justify' : 'start'};
      hyphens: auto; -webkit-hyphens: auto;
      widows: 2; orphans: 2;
    }
    [align="center"] { text-align: center; } [align="right"] { text-align: right; }
    pre { white-space: pre-wrap !important; }
    img, svg { max-width: 100%; }
    ${s.theme === 'dark' ? 'img { filter: brightness(.88); }' : ''}
  `
}

let current = null // { view, book, save }

export async function showReader(view, id) {
  closeReader()
  const s = settings()
  const root = h('div', { class: 'reader immersive', 'data-theme': s.theme })
  const loading = h('div', { class: 'loading' }, 'Abriendo…')
  root.append(loading)
  view.replaceChildren(root)

  // Metadatos (red o copia local) y posición más reciente.
  let book, serverLast
  try {
    ({ book } = await api(`api/books/${id}`))
    serverLast = book.last
  } catch (e) {
    if (!(e instanceof Offline)) { loading.textContent = e.message; return }
    book = cachedLibrary().find(b => b.id === id)
    serverLast = book?.last
    if (!book) { loading.textContent = 'Sin conexión y sin datos de este libro.'; return }
  }
  document.title = book.title
  const local = await localProgress(id)
  const start = [serverLast, local].filter(Boolean).sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0]

  let file
  try {
    // Los libros que se abren quedan guardados para leer sin conexión, salvo los
    // muy grandes (se pueden guardar a mano desde la ficha).
    file = await loadFile(book, { keep: book.size < 50 * 1024 * 1024, onProgress: f => { loading.textContent = `Descargando ${pct(f)}` } })
  } catch (e) {
    loading.textContent = e.message
    return
  }

  await import('../vendor/foliate/view.js')
  const fv = document.createElement('foliate-view')
  root.prepend(fv)
  try {
    await fv.open(file)
  } catch (e) {
    console.error(e)
    loading.textContent = 'No se pudo abrir el libro.'
    return
  }
  loading.remove()

  const ui = buildUI(root, fv, book)
  applySettings(fv, root, s)

  let saveTimer, lastSaved = ''
  const save = (loc, immediate) => {
    clearTimeout(saveTimer)
    const run = async () => {
      if (!loc?.cfi || loc.cfi === lastSaved) return
      lastSaved = loc.cfi
      const unit = ['pdf', 'cbz'].includes(book.format) ? 'pág.' : 'pos.'
      const label = loc.tocItem?.label?.trim() || (loc.location ? `${unit} ${loc.location.current + 1} de ${loc.location.total}` : '')
      await saveProgress(id, { location: loc.cfi, fraction: Math.min(1, Math.max(0, loc.fraction ?? 0)), label })
    }
    if (immediate) return run()
    saveTimer = setTimeout(run, 1500)
  }
  fv.addEventListener('relocate', e => {
    ui.update(e.detail)
    save(e.detail)
  })
  fv.addEventListener('load', e => wireDocument(e.detail.doc, fv, root))
  // Toques fuera del documento (márgenes, hueco entre páginas de un PDF).
  fv.addEventListener('click', e => { if (e.target === fv) root.classList.toggle('immersive') })

  await fv.init({ lastLocation: start?.location, showTextStart: !start })
  if (start && start.device_id && start !== local) {
    const who = start.device_name ?? 'otro dispositivo'
    toast(`Continuando donde lo dejaste en ${who} (${pct(start.fraction)})`)
  }

  const onKey = e => keyNav(e, fv, root)
  document.addEventListener('keydown', onKey)
  const onHide = () => { if (document.visibilityState === 'hidden') save(fv.lastLocation, true) }
  document.addEventListener('visibilitychange', onHide)
  current = { fv, save, cleanup: () => {
    document.removeEventListener('keydown', onKey)
    document.removeEventListener('visibilitychange', onHide)
  } }
}

export function closeReader() {
  if (!current) return
  current.save(current.fv.lastLocation, true)
  current.cleanup()
  current.fv.close?.()
  current.fv.remove()
  current = null
}

function applySettings(fv, root, s) {
  root.dataset.theme = s.theme
  const r = fv.renderer
  r.setStyles?.(css(s))
  r.setAttribute('flow', s.flow)
  r.setAttribute('margin', margins[s.margin][0])
  r.setAttribute('gap', margins[s.margin][1])
  r.setAttribute('max-inline-size', '720px')
  r.setAttribute('max-column-count', '2')
  document.querySelector('meta[name=theme-color]')?.setAttribute('content', themes[s.theme][0])
}

// Toques dentro del libro: tercio izquierdo atrás, derecho adelante, centro
// muestra u oculta las barras. Se ignoran los toques en enlaces y selecciones.
function wireDocument(doc, fv, root) {
  doc.addEventListener('keydown', e => keyNav(e, fv, root))
  doc.addEventListener('click', e => {
    if (e.target.closest?.('a[href]') || doc.getSelection()?.toString()) return
    // El iframe del libro puede ser más ancho que la pantalla (columnas en modo
    // paginado): se pasa el toque a coordenadas de la ventana del lector.
    const frame = doc.defaultView.frameElement
    const x = (frame?.getBoundingClientRect().left ?? 0) + e.clientX
    const rel = (x - root.getBoundingClientRect().left) / root.clientWidth
    if (fv.renderer.getAttribute('flow') === 'scrolled' || (rel > 0.3 && rel < 0.7)) root.classList.toggle('immersive')
    else if (rel <= 0.3) fv.goLeft()
    else fv.goRight()
  })
}

function keyNav(e, fv, root) {
  if (e.target.closest?.('input, select, textarea')) return
  if (e.key === 'ArrowLeft' || e.key === 'PageUp') fv.goLeft()
  else if (e.key === 'ArrowRight' || e.key === 'PageDown' || e.key === ' ') fv.goRight()
  else if (e.key === 'Escape') root.classList.toggle('immersive')
}

function buildUI(root, fv, book) {
  const slider = h('input', { type: 'range', min: 0, max: 1, step: 'any', value: 0, 'aria-label': 'Progreso',
    oninput: e => fv.goToFraction(parseFloat(e.target.value)) })
  const where = h('span')
  const percent = h('span')
  const peek = h('div', { class: 'peek' })
  const top = h('div', { class: 'rbar top' },
    h('button', { class: 'icon-btn', 'aria-label': 'Volver a la biblioteca', onclick: () => { location.hash = '#/' } }, icon('back')),
    h('div', { class: 'title' }, book.title),
    h('button', { class: 'icon-btn', 'aria-label': 'Índice', onclick: () => openTOC(root, fv) }, icon('toc')),
    h('button', { class: 'icon-btn', 'aria-label': 'Ajustes de lectura', onclick: () => openSettings(root, fv) }, icon('type')))
  const bottom = h('div', { class: 'rbar bottom' }, slider, h('div', { class: 'loc' }, where, percent))
  root.append(top, bottom, peek)
  // Al entrar se ven las barras unos segundos para orientarse.
  root.classList.remove('immersive')
  setTimeout(() => root.classList.add('immersive'), 2500)
  return {
    update(d) {
      slider.value = d.fraction ?? 0
      where.textContent = d.tocItem?.label?.trim() ?? ''
      percent.textContent = pct(d.fraction)
      peek.textContent = pct(d.fraction)
      currentHref = d.tocItem?.href
    },
  }
}

let currentHref
function panel(root, side, title, body) {
  const close = () => { scrim.remove(); p.remove() }
  const scrim = h('div', { class: 'scrim', onclick: close })
  const p = h('div', { class: `panel ${side}` },
    h('div', { style: 'display:flex;align-items:center' }, h('h3', { style: 'flex:1' }, title),
      h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: close }, icon('close'))),
    body(close))
  root.append(scrim, p)
}

function openTOC(root, fv) {
  const toc = fv.book.toc ?? []
  const build = (items, close) => h('ul', {}, ...items.map(item => h('li', {},
    h('a', { href: '#', 'aria-current': String(item.href === currentHref), onclick: e => {
      e.preventDefault()
      fv.goTo(item.href).catch(console.error)
      close()
    } }, item.label?.trim() || 'Sin título'),
    item.subitems?.length ? build(item.subitems, close) : null)))
  panel(root, 'left', 'Índice', close => toc.length ? h('nav', { class: 'toc' }, build(toc, close)) : h('p', {}, 'Este libro no tiene índice.'))
}

function openSettings(root, fv) {
  const s = settings()
  const set = (k, v) => { s[k] = v; saveSettings(s); applySettings(fv, root, s); render() }
  const seg = (k, opts) => h('div', { class: 'seg' }, ...opts.map(([v, label]) =>
    h('button', { 'aria-pressed': String(s[k] === v), onclick: () => set(k, v) }, label)))
  const body = h('div')
  function render() {
    body.replaceChildren(
      h('div', { class: 'row' }, h('span', {}, 'Tema'), seg('theme', [['light', 'Claro'], ['sepia', 'Sepia'], ['dark', 'Oscuro']])),
      h('div', { class: 'row' }, h('span', {}, `Tamaño · ${s.size} %`),
        h('div', { class: 'seg' },
          h('button', { onclick: () => set('size', Math.max(70, s.size - 10)), 'aria-label': 'Más pequeño' }, 'A−'),
          h('button', { onclick: () => set('size', Math.min(200, s.size + 10)), 'aria-label': 'Más grande' }, 'A+'))),
      h('div', { class: 'row' }, h('span', {}, 'Letra'), seg('font', [['original', 'Del libro'], ['serif', 'Serif'], ['sans', 'Sans']])),
      h('div', { class: 'row' }, h('span', {}, 'Interlineado'), seg('spacing', [[1.3, 'Compacto'], [1.5, 'Normal'], [1.8, 'Amplio']])),
      h('div', { class: 'row' }, h('span', {}, 'Márgenes'), seg('margin', [['narrow', 'Estrechos'], ['normal', 'Normales'], ['wide', 'Amplios']])),
      h('div', { class: 'row' }, h('span', {}, 'Alineación'), seg('justify', [[true, 'Justificado'], [false, 'Izquierda']])),
      h('div', { class: 'row' }, h('span', {}, 'Modo'), seg('flow', [['paginated', 'Páginas'], ['scrolled', 'Desplazamiento']])),
      h('div', { class: 'row' }, h('span', {}, 'Nombre de este dispositivo'),
        h('input', { type: 'text', value: localStorage.getItem('libros.device.name') ?? '', placeholder: 'Por ejemplo: Tablet del salón',
          onchange: e => { localStorage.setItem('libros.device.name', e.target.value.trim()); toast('Nombre guardado') } })),
    )
  }
  render()
  panel(root, 'right', 'Lectura', () => body)
}
