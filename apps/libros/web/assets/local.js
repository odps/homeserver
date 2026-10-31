// Estado local del dispositivo: progreso pendiente de enviar (cola sin
// conexión), última posición conocida y libros guardados para leer sin red.
import { api, device, me, Offline } from './util.js'

const DB = 'libros'
let dbp
function db() {
  dbp ??= new Promise((resolve, reject) => {
    const req = indexedDB.open(DB, 1)
    req.onupgradeneeded = () => {
      req.result.createObjectStore('queue')   // book id → progreso por enviar
      req.result.createObjectStore('local')   // book id → última posición en este dispositivo
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return dbp
}

async function tx(store, mode, fn) {
  const d = await db()
  return new Promise((resolve, reject) => {
    const t = d.transaction(store, mode)
    const req = fn(t.objectStore(store))
    t.oncomplete = () => resolve(req?.result)
    t.onerror = () => reject(t.error)
  })
}

const get = (store, key) => tx(store, 'readonly', s => s.get(key))
const put = (store, key, val) => tx(store, 'readwrite', s => s.put(val, key))
const del = (store, key) => tx(store, 'readwrite', s => s.delete(key))
const keys = store => tx(store, 'readonly', s => s.getAllKeys())

// Las claves llevan la persona delante: «usuario|id».
const key = id => `${me.user}|${id}`

export const localProgress = id => get('local', key(id))

// saveProgress guarda la posición en el dispositivo y la intenta enviar; si no
// hay red queda en cola. Devuelve la última posición según el servidor.
export async function saveProgress(id, { location, fraction, label }) {
  const d = device()
  const p = { device_id: d.id, device_name: d.name, location, fraction, label, updated_at: new Date().toISOString() }
  await put('local', key(id), p)
  await put('queue', key(id), p)
  return flushOne(id, p)
}

async function flushOne(id, p) {
  try {
    const { last } = await api(`api/books/${id}/progress`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
    })
    // Solo se quita de la cola si no ha llegado otra posición mientras tanto.
    const queued = await get('queue', key(id))
    if (queued?.updated_at === p.updated_at) await del('queue', key(id))
    return last
  } catch (e) {
    if (!(e instanceof Offline)) console.warn('progreso', e)
    return null
  }
}

let flushing
export function flushQueue() {
  flushing ??= (async () => {
    try {
      // Solo la cola de quien tiene la sesión abierta.
      for (const k of await keys('queue')) {
        const [user, id] = String(k).split('|')
        if (user !== me.user) continue
        const p = await get('queue', k)
        if (p) await flushOne(Number(id), p)
      }
    } finally {
      flushing = null
    }
  })()
  return flushing
}

export const pendingCount = async () => (await keys('queue')).filter(k => String(k).startsWith(`${me.user}|`)).length

// Libros guardados en este dispositivo (Cache Storage, el mismo que usa el
// service worker para servirlos sin conexión).
const BOOKS = 'libros-books'
export const fileURL = id => new URL(`api/books/${id}/file`, location.href).href

export async function isDownloaded(id) {
  if (!('caches' in window)) return false
  const c = await caches.open(BOOKS)
  return !!(await c.match(fileURL(id)))
}

export async function downloadedIds() {
  if (!('caches' in window)) return new Set()
  const c = await caches.open(BOOKS)
  const ids = (await c.keys()).map(r => Number(r.url.match(/books\/(\d+)\/file/)?.[1]))
  return new Set(ids.filter(Boolean))
}

export async function removeDownload(id) {
  const c = await caches.open(BOOKS)
  await c.delete(fileURL(id))
}

// loadFile devuelve el libro como File: del dispositivo si está guardado o de
// la red (informando del avance). Con keep, lo deja guardado para sin conexión.
export async function loadFile(book, { keep = false, onProgress } = {}) {
  const url = fileURL(book.id)
  const cache = 'caches' in window ? await caches.open(BOOKS) : null
  let resp = await cache?.match(url)
  if (!resp) {
    try {
      resp = await fetch(url)
    } catch {
      throw new Offline('Este libro no está guardado en el dispositivo y no hay conexión.')
    }
    if (resp.status === 401) throw new Error('sesión caducada')
    if (!resp.ok) throw new Error(`No se pudo descargar (${resp.status})`)
    const total = Number(resp.headers.get('Content-Length')) || book.size
    const reader = resp.body.getReader()
    const chunks = []
    let got = 0
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      chunks.push(value)
      got += value.length
      onProgress?.(got / total)
    }
    const blob = new Blob(chunks, { type: resp.headers.get('Content-Type') ?? '' })
    if (keep && cache) {
      await cache.put(url, new Response(blob, { headers: { 'Content-Type': blob.type, 'Content-Length': String(blob.size) } }))
    }
    return new File([blob], book.file_name, { type: blob.type })
  }
  const blob = await resp.blob()
  return new File([blob], book.file_name, { type: blob.type })
}

// Copia local de la biblioteca para verla sin conexión.
export function rememberLibrary(books) {
  try { localStorage.setItem(`libros.library.${me.user}`, JSON.stringify(books)) } catch {}
  try { localStorage.setItem('libros.me', JSON.stringify(me)) } catch {}
}
export function cachedLibrary() {
  try { return JSON.parse(localStorage.getItem(`libros.library.${me.user}`)) ?? [] } catch { return [] }
}

