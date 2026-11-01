// Utilidades de la interfaz. Todo el DOM se crea con h(): los textos van
// siempre como texto, nunca como HTML, así que los metadatos de un libro no
// pueden inyectar código.

export const $ = (sel, root = document) => root.querySelector(sel)

export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag)
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v == null || v === false) continue
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v)
    else if (k === 'class') el.className = v
    else if (k === 'style') el.style.cssText = v
    else if (k in el && k !== 'list') el[k] = v
    else el.setAttribute(k, v === true ? '' : v)
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue
    el.append(c instanceof Node ? c : document.createTextNode(String(c)))
  }
  return el
}

// Iconos (trazos de 24 px estilo Lucide), creados como SVG.
const paths = {
  back: 'M15 18l-6-6 6-6',
  search: 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3',
  plus: 'M12 5v14M5 12h14',
  close: 'M18 6 6 18M6 6l12 12',
  toc: 'M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01',
  type: 'M4 7V4h16v3M9 20h6M12 4v16',
  download: 'M12 3v12M7 10l5 5 5-5M5 21h14',
  check: 'M20 6 9 17l-5-5',
  trash: 'M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14',
  edit: 'M12 20h9M16.5 3.5a2.1 2.1 0 1 1 3 3L7 19l-4 1 1-4z',
  lock: 'M7 11V7a5 5 0 0 1 10 0v4M5 11h14v10H5z',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18',
  user: 'M20 21a8 8 0 0 0-16 0M12 13a4 4 0 1 0 0-8 4 4 0 0 0 0 8z',
  layers: 'M12 2 2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5',
  home: 'M3 10.5 12 3l9 7.5M5 9v11h5v-6h4v6h5V9',
  book: 'M4 19.5V5a2 2 0 0 1 2-2h14v15H6.5A2.5 2.5 0 0 0 4 20.5 2.5 2.5 0 0 0 6.5 23H20M8 7h8',
  compass: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM15.5 8.5l-2 5-5 2 2-5z',
  heart: 'M12 20.5s-7.5-4.6-9.3-9.2C1.4 8 3.5 4.5 7 4.5c2 0 3.6 1.1 5 3 1.4-1.9 3-3 5-3 3.5 0 5.6 3.5 4.3 6.8-1.8 4.6-9.3 9.2-9.3 9.2z',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  filter: 'M4 6h16M7 12h10M10 18h4',
  chevron: 'M9 6l6 6-6 6',
  logout: 'M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 17l5-5-5-5M15 12H3',
  sparkle: 'M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z',
  external: 'M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5',
  eye: 'M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  play: 'M7 4v16l13-8z',
  key: 'M15 7a4 4 0 1 1-3.5 6L5 19.5V22H2v-3l6.5-6.5A4 4 0 0 1 15 7zM16 8h.01',
  device: 'M7 2h10a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1zM11 18h2',
}
export function icon(name) {
  const ns = 'http://www.w3.org/2000/svg'
  const svg = document.createElementNS(ns, 'svg')
  svg.setAttribute('viewBox', '0 0 24 24')
  svg.setAttribute('fill', 'none')
  svg.setAttribute('stroke', 'currentColor')
  svg.setAttribute('stroke-width', '2')
  svg.setAttribute('stroke-linecap', 'round')
  svg.setAttribute('stroke-linejoin', 'round')
  svg.setAttribute('aria-hidden', 'true')
  const p = document.createElementNS(ns, 'path')
  p.setAttribute('d', paths[name])
  svg.append(p)
  return svg
}

const rtf = new Intl.RelativeTimeFormat('es', { numeric: 'auto' })
export function ago(date) {
  const s = (new Date(date) - Date.now()) / 1000
  const units = [['year', 31536000], ['month', 2592000], ['week', 604800], ['day', 86400], ['hour', 3600], ['minute', 60]]
  for (const [u, n] of units) if (Math.abs(s) >= n) return rtf.format(Math.round(s / n), u)
  return 'ahora mismo'
}

export const pct = f => `${Math.round((f ?? 0) * 100)} %`
export const formatSize = n => n > 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.ceil(n / 1024)} KB`

// Identidad del dispositivo: un id aleatorio y un nombre que se puede cambiar.
export function device() {
  let id = localStorage.getItem('libros.device.id')
  if (!id) {
    id = crypto.randomUUID()
    localStorage.setItem('libros.device.id', id)
  }
  return { id, name: localStorage.getItem('libros.device.name') || guessDevice() }
}

function guessDevice() {
  const ua = navigator.userAgent
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) return 'iPad'
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? 'Móvil Android' : 'Tablet Android'
  if (/Windows/.test(ua)) return 'PC Windows'
  if (/Macintosh/.test(ua)) return 'Mac'
  if (/Linux/.test(ua)) return 'Linux'
  return 'Navegador'
}

// api llama al servidor. Si la sesión de la pasarela ha caducado (401),
// muestra el aviso para volver a entrar.
export class Offline extends Error {}
export async function api(path, opts = {}) {
  let resp
  try {
    resp = await fetch(path, { ...opts, headers: { Accept: 'application/json', ...(opts.headers ?? {}) } })
  } catch {
    throw new Offline('sin conexión')
  }
  if (resp.status === 401) {
    sessionExpired()
    throw new Error('sesión caducada')
  }
  if (!resp.ok) {
    let msg = `error ${resp.status}`
    try { msg = (await resp.json()).error ?? msg } catch {}
    throw new Error(msg)
  }
  return resp.status === 204 ? null : resp.json()
}

export function sessionExpired() {
  const next = encodeURIComponent(location.pathname + location.hash)
  banner(h('span', {}, 'La sesión ha caducado. ', h('a', { href: `_login?next=${next}` }, 'Entrar de nuevo')))
}

// Persona con la sesión abierta (la fija app.js al arrancar). Los datos locales
// del dispositivo se guardan por persona, por si varias comparten navegador.
export const me = { user: '', name: '', admin: false }

export function banner(content) {
  const b = $('#banner')
  b.replaceChildren(content ?? '')
  b.hidden = !content
}

let toastTimer
export function toast(msg) {
  const t = $('#toast')
  t.textContent = msg
  t.hidden = false
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { t.hidden = true }, 3500)
}

// Reduce una portada a 600 px de alto como máximo, en JPEG.
export async function shrinkCover(blob) {
  try {
    const bmp = await createImageBitmap(blob)
    const scale = Math.min(1, 600 / bmp.height)
    const canvas = new OffscreenCanvas(Math.round(bmp.width * scale), Math.round(bmp.height * scale))
    canvas.getContext('2d').drawImage(bmp, 0, 0, canvas.width, canvas.height)
    return await canvas.convertToBlob({ type: 'image/jpeg', quality: 0.85 })
  } catch {
    return blob
  }
}

// URL de la portada de un libro; cambia cuando se edita, para no ver la vieja.
export const coverURL = b => `api/books/${b.id}/cover?v=${Date.parse(b.updated_at) || 0}`

// ambient pone en el elemento el color medio de la portada (--ambient), para
// teñir el fondo de las fichas. Las portadas son del mismo origen: el canvas
// las puede leer.
const ambientCache = new Map()
export function ambient(img, el) {
  const apply = c => el.style.setProperty('--ambient', c)
  if (ambientCache.has(img.src)) return apply(ambientCache.get(img.src))
  const run = () => {
    try {
      const c = document.createElement('canvas')
      c.width = c.height = 12
      const ctx = c.getContext('2d', { willReadFrequently: true })
      ctx.drawImage(img, 0, 0, 12, 12)
      const d = ctx.getImageData(0, 0, 12, 12).data
      let r = 0, g = 0, b = 0, n = 0
      for (let i = 0; i < d.length; i += 4) {
        // Se ignoran los píxeles casi blancos o casi negros (márgenes y texto).
        const l = (d[i] + d[i + 1] + d[i + 2]) / 3
        if (l < 25 || l > 235) continue
        r += d[i]; g += d[i + 1]; b += d[i + 2]; n++
      }
      if (!n) return
      const color = `rgb(${Math.round(r / n)} ${Math.round(g / n)} ${Math.round(b / n)})`
      ambientCache.set(img.src, color)
      apply(color)
    } catch {}
  }
  if (img.complete && img.naturalWidth) run()
  else img.addEventListener('load', run, { once: true })
}

// plural escribe «1 libro», «3 libros».
export const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`

// menu crea un botón ⋯ con un menú de opciones ([icono, texto, acción, clase]).
// El menú se abre fuera del flujo de la página (las fichas recortan lo que
// sobresale): en pantallas estrechas, como hoja desde abajo; en las anchas,
// junto al botón y sin salirse de la ventana.
export function menu(items, label = 'Más opciones') {
  const list = h('div', { class: 'menu', role: 'menu' })
  const scrim = h('div', { class: 'menu-scrim' })
  let open = false
  const close = () => {
    if (!open) return
    open = false
    list.remove()
    scrim.remove()
    removeEventListener('resize', close)
    removeEventListener('scroll', onScroll, true)
    document.removeEventListener('keydown', onKey)
    document.removeEventListener('click', close)
  }
  const onScroll = e => { if (!list.contains(e.target)) close() }
  const onKey = e => { if (e.key === 'Escape') { close(); btn.focus() } }
  const btn = h('button', { class: 'btn icon-only', 'aria-label': label, 'aria-haspopup': 'menu', title: label, onclick: e => {
    e.stopPropagation()
    if (open) return close()
    open = true
    const sheet = innerWidth < 720
    list.classList.toggle('as-sheet', sheet)
    list.style.cssText = ''
    if (sheet) document.body.append(scrim)
    document.body.append(list)
    if (!sheet) {
      const r = btn.getBoundingClientRect()
      const w = list.offsetWidth, hgt = list.offsetHeight
      const left = Math.min(Math.max(8, r.right - w), innerWidth - w - 8)
      const below = r.bottom + 6 + hgt <= innerHeight - 8
      list.style.left = `${left}px`
      list.style.top = `${below ? r.bottom + 6 : Math.max(8, r.top - 6 - hgt)}px`
    }
    list.querySelector('a, button')?.focus({ preventScroll: true })
    addEventListener('resize', close)
    addEventListener('scroll', onScroll, true)
    document.addEventListener('keydown', onKey)
    setTimeout(() => document.addEventListener('click', close))
  } }, icon('more'))
  scrim.addEventListener('click', close)
  for (const it of items.filter(Boolean)) {
    if (it === '-') { list.append(h('hr')); continue }
    const [ic, text, action, cls] = it
    list.append(typeof action === 'string'
      ? h('a', { href: action, class: cls ?? '', role: 'menuitem', download: cls === 'download' ? '' : null, onclick: close }, icon(ic), text)
      : h('button', { type: 'button', class: cls ?? '', role: 'menuitem', onclick: () => { close(); action() } }, icon(ic), text))
  }
  return h('div', { class: 'menu-wrap' }, btn)
}

// pick es un desplegable compacto con icono (para filtros): options son
// [valor, texto]; onchange recibe el valor.
export function pick(iconName, label, value, options, onchange) {
  return h('label', { class: 'pick', title: label },
    icon(iconName),
    h('select', { 'aria-label': label, onchange: e => onchange(e.target.value) },
      ...options.map(([v, t]) => h('option', { value: v, selected: String(value) === String(v) }, t))))
}

// Color estable para las portadas generadas, a partir del título.
export function hue(text) {
  let n = 0
  for (const c of text) n = (n * 31 + c.codePointAt(0)) % 360
  return n
}
