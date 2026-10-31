// Biblioteca: «Continuar leyendo», filtros, cuadrícula de portadas, ficha del
// libro (con el progreso de cada dispositivo), edición y subida.
import { $, h, icon, api, ago, pct, formatSize, toast, hue, device, Offline } from './util.js'
import { rememberLibrary, cachedLibrary, downloadedIds, isDownloaded, loadFile, removeDownload, localProgress } from './local.js'

const statusLabel = { pendiente: 'Pendiente', leyendo: 'Leyendo', leido: 'Leído' }
const state = {
  q: '',
  status: localStorage.getItem('libros.filter') ?? '',
  sort: localStorage.getItem('libros.sort') ?? 'recent',
  books: [],
  offline: false,
}

export async function showLibrary(view) {
  document.title = 'Libros'
  view.replaceChildren(h('div', { class: 'library' }, h('div', { class: 'empty' }, 'Cargando…')))
  await refresh(view)
}

async function refresh(view) {
  const params = new URLSearchParams({ sort: state.sort })
  if (state.status) params.set('status', state.status)
  if (state.q) params.set('q', state.q)
  try {
    state.books = await api(`api/books?${params}`)
    state.offline = false
    if (!state.q && !state.status) rememberLibrary(state.books)
  } catch (e) {
    if (!(e instanceof Offline)) {
      toast(e.message)
      return
    }
    state.offline = true
    state.books = filterLocal(cachedLibrary())
  }
  render(view, await downloadedIds())
}

// Sin conexión, filtra la última copia de la biblioteca en el dispositivo.
function filterLocal(books) {
  const q = state.q.toLowerCase()
  return books.filter(b => (!state.status || b.status === state.status) &&
    (!q || [b.title, ...b.authors, ...b.genres, b.series ?? ''].some(s => s.toLowerCase().includes(q))))
}

function render(view, downloaded) {
  const all = state.books
  const current = !state.q && !state.status ? all.find(b => b.last && b.status !== 'leido') : null
  const root = h('div', { class: 'library' },
    h('div', { class: 'top' },
      h('h1', {}, 'Libros'),
      h('div', { class: 'search' }, icon('search'),
        h('input', { type: 'search', placeholder: 'Título, autor, género…', value: state.q, 'aria-label': 'Buscar',
          oninput: debounce(e => { state.q = e.target.value.trim(); refresh(view) }, 300) })),
      h('button', { class: 'btn primary', onclick: () => openUpload(view), disabled: state.offline }, icon('plus'), 'Añadir'),
    ),
    current && continueCard(current),
    h('div', { class: 'filters' },
      ...[['', 'Todos'], ['leyendo', 'Leyendo'], ['pendiente', 'Pendientes'], ['leido', 'Leídos']].map(([v, label]) =>
        h('button', { class: 'chip', 'aria-pressed': String(state.status === v), onclick: () => {
          state.status = v
          localStorage.setItem('libros.filter', v)
          refresh(view)
        } }, label)),
      h('select', { 'aria-label': 'Ordenar', onchange: e => {
        state.sort = e.target.value
        localStorage.setItem('libros.sort', state.sort)
        refresh(view)
      } }, ...[['recent', 'Leídos recientemente'], ['added', 'Añadidos recientemente'], ['title', 'Título'], ['author', 'Autor']]
        .map(([v, label]) => h('option', { value: v, selected: state.sort === v }, label))),
    ),
    all.length ? h('div', { class: 'grid' }, ...all.map(b => tile(b, downloaded.has(b.id), view))) : emptyState(view),
  )
  view.replaceChildren(root)
}

function emptyState(view) {
  if (state.q || state.status) return h('div', { class: 'empty' }, 'Ningún libro coincide.')
  return h('div', { class: 'empty' },
    h('h2', {}, 'Tu biblioteca está vacía'),
    h('p', {}, 'Añade tus libros (EPUB, PDF, MOBI, AZW3, FB2, CBZ) y léelos desde cualquier dispositivo.'),
    h('button', { class: 'btn primary', onclick: () => openUpload(view), disabled: state.offline }, icon('plus'), 'Añadir libros'))
}

export function cover(b, badge) {
  const c = h('div', { class: 'cover' })
  const ph = h('div', { class: 'ph', style: `--c1: hsl(${hue(b.title)} 30% 42%); --c2: hsl(${hue(b.title)} 35% 24%)` },
    h('b', {}, b.title), h('small', {}, b.authors?.[0] ?? ''))
  c.append(ph)
  if (b.has_cover) {
    const img = h('img', { src: `api/books/${b.id}/cover`, alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  if (b.format !== 'epub') c.append(h('span', { class: 'fmt' }, b.format))
  if (badge) c.append(h('span', { class: 'badge', title: 'Guardado en este dispositivo' }, '✓'))
  return c
}

function tile(b, downloaded, view) {
  return h('div', { class: 'book', tabindex: 0, role: 'button', 'aria-label': b.title,
    onclick: () => openDetail(b.id, view), onkeydown: e => e.key === 'Enter' && openDetail(b.id, view) },
    cover(b, downloaded),
    b.last && b.status !== 'leido' ? h('div', { class: 'bar', title: pct(b.last.fraction) }, h('i', { style: `width:${b.last.fraction * 100}%` })) : null,
    h('div', { class: 't' }, b.title),
    h('div', { class: 'a' }, b.authors.join(', ') || '—'))
}

function continueCard(b) {
  const where = b.last.device_id === device().id ? 'en este dispositivo' : `en ${b.last.device_name}`
  return h('div', { class: 'continue', role: 'button', tabindex: 0, onclick: () => { location.hash = `#/leer/${b.id}` },
    onkeydown: e => e.key === 'Enter' && (location.hash = `#/leer/${b.id}`) },
    cover(b),
    h('div', { class: 'info' },
      h('div', { class: 'eyebrow' }, 'Continuar leyendo'),
      h('h2', {}, b.title),
      h('div', { class: 'meta' }, b.authors.join(', ')),
      h('div', { class: 'bar' }, h('i', { style: `width:${b.last.fraction * 100}%` })),
      h('div', { class: 'where' }, `${pct(b.last.fraction)}${b.last.label ? ' · ' + b.last.label : ''} · ${where} ${ago(b.last.updated_at)}`)),
    h('button', { class: 'btn primary', onclick: e => { e.stopPropagation(); location.hash = `#/leer/${b.id}` } }, 'Continuar'))
}

// Ficha del libro.
async function openDetail(id, view) {
  const dlg = $('#dialog')
  let book, devices = []
  try {
    ({ book, devices } = await api(`api/books/${id}`))
  } catch (e) {
    if (!(e instanceof Offline)) return toast(e.message)
    book = cachedLibrary().find(b => b.id === id)
    if (!book) return toast('Sin conexión')
  }
  const local = await localProgress(id)
  const downloaded = await isDownloaded(id)
  const facts = [book.format.toUpperCase(), formatSize(book.size), book.language, book.publisher, book.published].filter(Boolean)
  const statusSel = h('select', { 'aria-label': 'Estado', onchange: async e => {
    try {
      await api(`api/books/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...editable(book), status: e.target.value }) })
      toast('Estado actualizado')
      refresh(view)
    } catch (err) { toast(err.message) }
  } }, ...Object.entries(statusLabel).map(([v, l]) => h('option', { value: v, selected: book.status === v }, l)))

  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Libro'), h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))),
    h('div', { class: 'detail' },
      cover(book, downloaded),
      h('div', {},
        h('h3', {}, book.title),
        h('p', {}, book.authors.join(', ') || 'Autor desconocido'),
        book.series ? h('p', { class: 'meta' }, `${book.series}${book.series_index != null ? ' #' + book.series_index : ''}`) : null,
        book.genres.length ? h('p', { class: 'meta' }, book.genres.join(' · ')) : null,
        h('p', { class: 'meta' }, facts.join(' · ')),
      )),
    book.description ? h('div', { class: 'desc' }, book.description) : null,
    h('div', { class: 'actions' },
      h('button', { class: 'btn primary', onclick: () => { dlg.close(); location.hash = `#/leer/${id}` } },
        book.last || local ? 'Continuar' : 'Leer'),
      downloaded
        ? h('button', { class: 'btn', onclick: async () => { await removeDownload(id); toast('Quitado de este dispositivo'); dlg.close(); refresh(view) } }, 'Quitar del dispositivo')
        : h('button', { class: 'btn', onclick: async e => {
          const btn = e.currentTarget
          btn.disabled = true
          try {
            await loadFile(book, { keep: true, onProgress: f => { btn.textContent = `Descargando ${pct(f)}` } })
            toast('Guardado para leer sin conexión')
            dlg.close()
            refresh(view)
          } catch (err) { toast(err.message); btn.disabled = false }
        } }, icon('download'), 'Guardar sin conexión'),
      statusSel,
    ),
    devices.length ? h('div', {},
      h('label', {}, 'Progreso por dispositivo'),
      h('ul', { class: 'devices' }, ...devices.map(d => h('li', {},
        h('span', {}, d.device_id === device().id ? `${d.device_name} (este)` : d.device_name),
        h('span', {}, pct(d.fraction)),
        h('span', { class: 'meta' }, ago(d.updated_at)))))) : null,
    h('div', { class: 'actions' },
      h('button', { class: 'btn', onclick: () => openEdit(book, view) }, icon('edit'), 'Editar'),
      h('a', { class: 'btn', href: `api/books/${id}/file`, download: book.file_name }, 'Descargar fichero'),
      h('button', { class: 'btn danger', onclick: async () => {
        if (!confirm(`¿Borrar «${book.title}» de la biblioteca? Se borra el fichero y el progreso en todos los dispositivos.`)) return
        try {
          await api(`api/books/${id}`, { method: 'DELETE' })
          await removeDownload(id)
          dlg.close()
          toast('Libro borrado')
          refresh(view)
        } catch (err) { toast(err.message) }
      } }, icon('trash'), 'Borrar'),
    ),
  ))
  if (!dlg.open) dlg.showModal()
}

const editable = b => ({
  title: b.title, authors: b.authors, genres: b.genres, series: b.series ?? '', series_index: b.series_index,
  language: b.language ?? '', publisher: b.publisher ?? '', published: b.published ?? '', description: b.description ?? '',
})

function openEdit(book, view) {
  const dlg = $('#dialog')
  const f = name => h('input', { type: 'text', name, value: (Array.isArray(book[name]) ? book[name].join(', ') : book[name]) ?? '' })
  const form = h('form', { class: 'form', onsubmit: async e => {
    e.preventDefault()
    const d = new FormData(form)
    const split = s => String(s).split(',').map(x => x.trim()).filter(Boolean)
    const idx = parseFloat(d.get('series_index'))
    const body = {
      title: d.get('title'), authors: split(d.get('authors')), genres: split(d.get('genres')),
      series: d.get('series'), series_index: Number.isFinite(idx) ? idx : null, language: d.get('language'),
      publisher: d.get('publisher'), published: d.get('published'), description: d.get('description'), status: book.status,
    }
    try {
      await api(`api/books/${book.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      toast('Guardado')
      dlg.close()
      refresh(view)
    } catch (err) { toast(err.message) }
  } },
    h('label', { class: 'full' }, 'Título', f('title')),
    h('label', { class: 'full' }, 'Autores (separados por comas)', f('authors')),
    h('label', { class: 'full' }, 'Géneros (separados por comas)', f('genres')),
    h('label', {}, 'Serie', f('series')),
    h('label', {}, 'Número en la serie', h('input', { type: 'number', step: 'any', name: 'series_index', value: book.series_index ?? '' })),
    h('label', {}, 'Idioma', f('language')),
    h('label', {}, 'Año', f('published')),
    h('label', { class: 'full' }, 'Editorial', f('publisher')),
    h('label', { class: 'full' }, 'Descripción', h('textarea', { name: 'description', rows: 5 }, book.description ?? '')),
    h('div', { class: 'actions full' }, h('button', { class: 'btn primary' }, 'Guardar'), h('button', { class: 'btn', type: 'button', onclick: () => openDetail(book.id, view) }, 'Cancelar')),
  )
  dlg.replaceChildren(h('div', { class: 'sheet' }, h('header', {}, h('h2', {}, 'Editar libro')), form))
}

// Subida: metadatos y portada se extraen aquí con foliate-js.
const accept = '.epub,.pdf,.mobi,.azw3,.azw,.fb2,.fbz,.cbz'
function openUpload(view) {
  const dlg = $('#dialog')
  const queue = h('ul', { class: 'queue' })
  const input = h('input', { type: 'file', accept, multiple: true, hidden: true, onchange: e => add(e.target.files) })
  const drop = h('div', { class: 'drop', role: 'button', tabindex: 0, onclick: () => input.click(),
    ondragover: e => { e.preventDefault(); drop.classList.add('over') },
    ondragleave: () => drop.classList.remove('over'),
    ondrop: e => { e.preventDefault(); drop.classList.remove('over'); add(e.dataTransfer.files) } },
    h('p', {}, h('strong', {}, 'Arrastra tus libros aquí'), h('br'), 'o pulsa para elegirlos'),
    h('p', { class: 'meta' }, 'EPUB, PDF, MOBI, AZW3, FB2, FBZ y CBZ · hasta 200 MB cada uno'))
  let added = 0
  let chain = Promise.resolve()
  function add(files) {
    for (const file of files) {
      const status = h('span', {}, 'En cola')
      const bar = h('i', { style: 'width:0' })
      queue.append(h('li', {}, h('div', { class: 'row' }, h('span', {}, file.name), status), h('div', { class: 'bar' }, bar)))
      // De uno en uno: el móvil y la conexión lo agradecen.
      chain = chain.then(() => uploadOne(file, status, bar)).then(ok => { if (ok) added++ })
    }
  }
  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Añadir libros'),
      h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => { dlg.close(); if (added) refresh(view) } }, icon('close'))),
    drop, input, queue))
  dlg.onclose = () => { if (added) refresh(view); dlg.onclose = null }
  dlg.showModal()
}

async function uploadOne(file, status, bar) {
  if (file.size > 200 * 1024 * 1024) {
    status.textContent = 'Supera 200 MB'
    status.className = 'err'
    return false
  }
  status.textContent = 'Leyendo metadatos…'
  let meta = {}, cover = null
  try {
    ({ meta, cover } = await extract(file))
  } catch (e) {
    console.warn('metadatos', e)
    status.textContent = 'Sin metadatos, subiendo…'
  }
  const form = new FormData()
  form.append('meta', JSON.stringify(meta))
  if (cover) form.append('cover', cover, 'cover.jpg')
  form.append('file', file, file.name)
  return new Promise(resolve => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', 'api/books')
    xhr.setRequestHeader('Accept', 'application/json')
    xhr.upload.onprogress = e => {
      if (e.lengthComputable) {
        bar.style.width = `${(e.loaded / e.total) * 100}%`
        status.textContent = `Subiendo ${pct(e.loaded / e.total)}`
      }
    }
    xhr.onload = () => {
      let res = {}
      try { res = JSON.parse(xhr.responseText) } catch {}
      if (xhr.status === 201 || xhr.status === 200) {
        bar.style.width = '100%'
        status.textContent = res.duplicate ? 'Ya estaba en la biblioteca' : `Añadido: ${res.book?.title ?? ''}`
        status.className = 'ok'
        resolve(!res.duplicate)
      } else {
        status.textContent = xhr.status === 401 ? 'Sesión caducada' : res.error ?? `Error ${xhr.status}`
        status.className = 'err'
        resolve(false)
      }
    }
    xhr.onerror = () => { status.textContent = 'Sin conexión'; status.className = 'err'; resolve(false) }
    xhr.send(form)
  })
}

// Se carga bajo demanda: foliate-js solo hace falta al subir o al leer.
async function extract(file) {
  const { extract } = await import('./meta.js')
  return extract(file)
}

function debounce(fn, ms) {
  let t
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms) }
}
