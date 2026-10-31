// Extracción de metadatos y portada en el navegador con foliate-js, antes de
// subir el libro. Así funciona igual para todos los formatos que lee.
import { makeBook } from '../vendor/foliate/view.js'

// Los metadatos siguen el esquema de Readium: los textos pueden ser una cadena
// o un mapa por idioma, y las personas una cadena, un objeto o una lista.
const text = x => typeof x === 'string' ? x : x && typeof x === 'object' ? Object.values(x)[0] ?? '' : ''
const list = x => (Array.isArray(x) ? x : x ? [x] : [])
const names = x => list(x).map(p => typeof p === 'string' ? p : text(p?.name ?? p?.label ?? p?.code)).filter(Boolean)

function stripHTML(html) {
  if (!html) return ''
  const doc = new DOMParser().parseFromString(String(html), 'text/html')
  return (doc.body.textContent ?? '').replace(/\n{3,}/g, '\n\n').trim()
}

function series(m) {
  const s = list(m.belongsTo?.series ?? m.belongsTo?.collection)[0]
  if (!s) return {}
  const name = typeof s === 'string' ? s : text(s.name)
  const pos = parseFloat(s.position)
  return { series: name, series_index: Number.isFinite(pos) ? pos : null }
}

function published(v) {
  if (!v) return ''
  const d = new Date(v)
  return isNaN(d) ? String(v) : String(d.getUTCFullYear())
}

// Reduce la portada a 600 px de alto como máximo, en JPEG.
async function shrink(blob) {
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

export async function extract(file) {
  const book = await makeBook(file)
  const m = book.metadata ?? {}
  const meta = {
    title: text(m.title),
    authors: names(m.author ?? m.creator),
    genres: names(m.subject),
    language: list(m.language)[0] ?? '',
    publisher: names(m.publisher)[0] ?? '',
    published: published(m.published),
    description: stripHTML(m.description),
    ...series(m),
  }
  let cover = null
  try {
    const blob = await book.getCover?.()
    if (blob) cover = await shrink(blob)
  } catch {}
  book.destroy?.()
  return { meta, cover }
}
