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
  user: 'M20 21a8 8 0 0 0-16 0M12 13a4 4 0 1 0 0-8 4 4 0 0 0 0 8z',
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

// Color estable para las portadas generadas, a partir del título.
export function hue(text) {
  let n = 0
  for (const c of text) n = (n * 31 + c.codePointAt(0)) % 360
  return n
}
