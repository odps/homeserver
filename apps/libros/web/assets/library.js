// Biblioteca: «Continuar leyendo», filtros, cuadrícula de portadas, ficha del
// libro (con el progreso de cada dispositivo), edición y subida.
import { $, h, icon, api, ago, pct, formatSize, toast, hue, device, me, coverURL, shrinkCover, Offline } from './util.js'
import { rememberLibrary, cachedLibrary, downloadedIds, isDownloaded, loadFile, removeDownload, localProgress } from './local.js'
import { canInstall, installed, install, installHelp, onInstallChange } from './install.js'

const statusLabel = { pendiente: 'Pendiente', leyendo: 'Leyendo', leido: 'Leído' }
const state = {
  q: '',
  status: localStorage.getItem('libros.filter') ?? '',
  sort: localStorage.getItem('libros.sort') ?? 'recent',
  scope: localStorage.getItem('libros.scope') ?? '',
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
  if (state.scope) params.set('scope', state.scope)
  if (state.q) params.set('q', state.q)
  try {
    state.books = await api(`api/books?${params}`)
    state.offline = false
    if (!state.q && !state.status && !state.scope) rememberLibrary(state.books)
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
    (!state.scope || (state.scope === 'mine' ? b.owner === me.user : b.visibility === 'compartido')) &&
    (!q || [b.title, ...b.authors, ...b.genres, b.series ?? '', b.isbn ?? ''].some(s => s.toLowerCase().includes(q))))
}

function render(view, downloaded) {
  const all = state.books
  const current = !state.q && !state.status && !state.scope ? all.find(b => b.last && b.status !== 'leido') : null
  const root = h('div', { class: 'library' },
    h('div', { class: 'top' },
      h('h1', {}, 'Libros'),
      h('div', { class: 'search' }, icon('search'),
        h('input', { type: 'search', placeholder: 'Título, autor, género…', value: state.q, 'aria-label': 'Buscar',
          oninput: debounce(e => { state.q = e.target.value.trim(); refresh(view) }, 300) })),
      h('a', { class: 'btn', href: '#/explorar', title: 'Buscar en bibliotecas públicas' }, icon('globe'), h('span', { class: 'hide-sm' }, 'Explorar')),
      h('button', { class: 'btn primary', onclick: () => openUpload(view), disabled: state.offline }, icon('plus'), h('span', { class: 'hide-sm' }, 'Añadir')),
      userMenu(),
    ),
    installCard(view),
    current && continueCard(current),
    h('div', { class: 'filters' },
      ...[['', 'Todos'], ['leyendo', 'Leyendo'], ['pendiente', 'Pendientes'], ['leido', 'Leídos']].map(([v, label]) =>
        h('button', { class: 'chip', 'aria-pressed': String(state.status === v), onclick: () => {
          state.status = v
          localStorage.setItem('libros.filter', v)
          refresh(view)
        } }, label)),
      h('select', { 'aria-label': 'Qué libros', onchange: e => {
        state.scope = e.target.value
        localStorage.setItem('libros.scope', state.scope)
        refresh(view)
      } }, ...[['', 'Todos los libros'], ['mine', 'Subidos por mí'], ['shared', 'Compartidos']]
        .map(([v, label]) => h('option', { value: v, selected: state.scope === v }, label))),
      h('select', { 'aria-label': 'Ordenar', onchange: e => {
        state.sort = e.target.value
        localStorage.setItem('libros.sort', state.sort)
        refresh(view)
      } }, ...[['recent', 'Leídos recientemente'], ['added', 'Añadidos recientemente'], ['title', 'Título'], ['author', 'Autor']]
        .map(([v, label]) => h('option', { value: v, selected: state.sort === v }, label))),
    ),
    all.length ? h('div', { class: 'grid' }, ...all.map(b => tile(b, downloaded.has(b.id), view))) : emptyState(view),
    h('footer', { class: 'account' },
      h('span', {}, 'Conectado como ', h('strong', {}, me.name || me.user)),
      h('a', { href: '_cuenta' }, 'Mi cuenta'),
      !installed() ? h('button', { type: 'button', onclick: () => installHelp() }, 'Instalar la app') : null,
      h('form', { method: 'post', action: '_logout' }, h('button', {}, 'Salir'))),
  )
  view.replaceChildren(root)
}

function emptyState(view) {
  if (state.q || state.status || state.scope) return h('div', { class: 'empty' }, 'Ningún libro coincide.')
  return h('div', { class: 'empty' },
    h('h2', {}, 'Tu biblioteca está vacía'),
    h('p', {}, 'Añade tus libros (EPUB, PDF, MOBI, AZW3, FB2, CBZ) o busca clásicos en bibliotecas públicas, y léelos desde cualquier dispositivo.'),
    h('div', { class: 'actions center' },
      h('button', { class: 'btn primary', onclick: () => openUpload(view), disabled: state.offline }, icon('plus'), 'Añadir libros'),
      h('a', { class: 'btn', href: '#/explorar' }, icon('globe'), 'Explorar bibliotecas públicas')))
}

export function cover(b, badge) {
  const c = h('div', { class: 'cover' })
  const ph = h('div', { class: 'ph', style: `--c1: hsl(${hue(b.title)} 30% 42%); --c2: hsl(${hue(b.title)} 35% 24%)` },
    h('b', {}, b.title), h('small', {}, b.authors?.[0] ?? ''))
  c.append(ph)
  if (b.has_cover) {
    const img = h('img', { src: coverURL(b), alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  if (b.format !== 'epub') c.append(h('span', { class: 'fmt' }, b.format))
  if (b.visibility === 'privado') c.append(h('span', { class: 'lock', title: 'Privado: solo lo ves tú' }, icon('lock')))
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
  const facts = [book.format.toUpperCase(), formatSize(book.size), book.language, book.publisher, book.published, book.isbn && `ISBN ${book.isbn}`].filter(Boolean)
  const statusSel = h('select', { 'aria-label': 'Estado', onchange: async e => {
    try {
      await api(`api/books/${id}/status`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status: e.target.value }) })
      toast('Estado actualizado')
      refresh(view)
    } catch (err) { toast(err.message) }
  } }, ...Object.entries(statusLabel).map(([v, l]) => h('option', { value: v, selected: book.status === v }, l)))

  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Libro'),
      book.can_edit && !state.offline ? h('button', { class: 'icon-btn', 'aria-label': 'Editar datos', title: 'Editar datos', onclick: () => openEdit(book, view) }, icon('edit')) : null,
      h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))),
    h('div', { class: 'detail' },
      cover(book, downloaded),
      h('div', {},
        h('h3', {}, book.title),
        h('p', {}, book.authors.join(', ') || 'Autor desconocido'),
        book.series ? h('p', { class: 'meta' }, `${book.series}${book.series_index != null ? ' #' + book.series_index : ''}`) : null,
        book.genres.length ? h('p', { class: 'meta' }, book.genres.join(' · ')) : null,
        h('p', { class: 'meta' }, facts.join(' · ')),
        h('p', { class: 'meta' }, book.visibility === 'privado' ? 'Privado: solo lo ves tú'
          : `Compartido${book.owner === me.user ? ' por ti' : book.owner ? ` · subido por ${book.owner}` : ''}`),
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
      book.can_edit ? h('button', { class: 'btn', onclick: () => openEdit(book, view) }, icon('edit'), 'Editar') : null,
      book.owner === me.user ? h('button', { class: 'btn', onclick: async () => {
        const visibility = book.visibility === 'privado' ? 'compartido' : 'privado'
        try {
          await api(`api/books/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...editable(book), visibility }) })
          toast(visibility === 'privado' ? 'Ahora es privado' : 'Ahora lo ven todos')
          openDetail(id, view)
          refresh(view)
        } catch (err) { toast(err.message) }
      } }, book.visibility === 'privado' ? 'Compartir con todos' : 'Hacer privado') : null,
      h('a', { class: 'btn', href: `api/books/${id}/file`, download: book.file_name }, 'Descargar fichero'),
      !book.can_edit ? null : h('button', { class: 'btn danger', onclick: async () => {
        if (!confirm(`¿Borrar «${book.title}» de la biblioteca? ${book.visibility === 'compartido' ? 'Desaparece para todos y se pierde el progreso de cada persona.' : 'Se borra también tu progreso.'}`)) return
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
  isbn: b.isbn ?? '',
})

function openEdit(book, view) {
  const dlg = $('#dialog')
  const f = name => h('input', { type: 'text', name, value: (Array.isArray(book[name]) ? book[name].join(', ') : book[name]) ?? '' })
  let newCover = null      // imagen elegida a mano
  let coverFromISBN = null // ISBN cuya portada se usará al guardar
  const preview = cover(book)
  const coverInput = h('input', { type: 'file', accept: 'image/*', hidden: true, onchange: async e => {
    const file = e.target.files[0]
    if (!file) return
    newCover = await shrinkCover(file)
    coverFromISBN = null
    showCover(URL.createObjectURL(newCover))
  } })
  const showCover = src => {
    preview.querySelector('img')?.remove()
    preview.querySelector('.fmt.new')?.remove()
    preview.append(h('img', { src, alt: '' }))
  }
  const found = h('div', { class: 'found', hidden: true })
  const isbnInput = h('input', { type: 'text', name: 'isbn', inputmode: 'numeric', value: book.isbn ?? '', placeholder: '978…' })
  const lookup = async e => {
    const btn = e.currentTarget
    const v = isbnInput.value.trim()
    if (!v) return toast('Escribe el ISBN')
    btn.disabled = true
    try {
      const info = await api(`api/isbn/${encodeURIComponent(v)}`)
      isbnInput.value = info.isbn
      showFound(info)
    } catch (err) { toast(err.message) }
    btn.disabled = false
  }
  function showFound(info) {
    const useCover = h('input', { type: 'checkbox', checked: info.has_cover })
    const rows = [['Título', info.title], ['Autores', info.authors?.join(', ')], ['Editorial', info.publisher], ['Año', info.published],
      ['Géneros', info.genres?.join(', ')], ['Idioma', info.language]].filter(([, v]) => v)
    found.hidden = false
    found.replaceChildren(
      h('strong', {}, 'Datos encontrados'),
      h('dl', {}, ...rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
      info.has_cover ? h('label', { class: 'check' }, useCover, 'Usar también su portada') : null,
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'btn small primary', onclick: () => {
          const set = (name, v) => { if (v) form.elements[name].value = Array.isArray(v) ? v.join(', ') : v }
          set('title', info.title); set('authors', info.authors); set('publisher', info.publisher); set('published', info.published)
          set('genres', info.genres); set('language', info.language); set('description', info.description)
          if (info.has_cover && useCover.checked) {
            coverFromISBN = info.isbn
            newCover = null
            preview.querySelector('img')?.remove()
            preview.querySelector('.fmt.new')?.remove()
            preview.append(h('span', { class: 'fmt new' }, 'Nueva'))
          }
          found.hidden = true
          toast('Datos copiados al formulario: revisa y guarda')
        } }, 'Usar estos datos'),
        h('button', { type: 'button', class: 'btn small', onclick: () => { found.hidden = true } }, 'Descartar')))
  }
  const form = h('form', { class: 'form', onsubmit: async e => {
    e.preventDefault()
    const d = new FormData(form)
    const split = s => String(s).split(',').map(x => x.trim()).filter(Boolean)
    const idx = parseFloat(d.get('series_index'))
    const body = {
      title: d.get('title'), authors: split(d.get('authors')), genres: split(d.get('genres')),
      series: d.get('series'), series_index: Number.isFinite(idx) ? idx : null, language: d.get('language'),
      publisher: d.get('publisher'), published: d.get('published'), description: d.get('description'), isbn: d.get('isbn'),
    }
    const btn = e.submitter
    if (btn) btn.disabled = true
    try {
      await api(`api/books/${book.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (newCover) await api(`api/books/${book.id}/cover`, { method: 'PUT', headers: { 'Content-Type': newCover.type || 'image/jpeg' }, body: newCover })
      else if (coverFromISBN) await api(`api/books/${book.id}/cover?isbn=${encodeURIComponent(coverFromISBN)}`, { method: 'PUT' })
      toast('Guardado')
      openDetail(book.id, view)
      refresh(view)
    } catch (err) {
      toast(err.message)
      if (btn) btn.disabled = false
    }
  } },
    h('div', { class: 'full cover-edit' }, preview,
      h('div', {},
        h('button', { type: 'button', class: 'btn small', onclick: () => coverInput.click() }, icon('edit'), 'Cambiar portada'),
        h('p', { class: 'meta' }, 'Una imagen de tu dispositivo, o la que se encuentre por el ISBN.')),
      coverInput),
    h('label', { class: 'full' }, 'Título', f('title')),
    h('label', { class: 'full' }, 'Autores (separados por comas)', f('authors')),
    h('label', { class: 'full' }, 'ISBN',
      h('div', { class: 'inline' }, isbnInput,
        h('button', { type: 'button', class: 'btn small', onclick: lookup }, icon('search'), 'Buscar datos'))),
    found,
    h('label', { class: 'full' }, 'Géneros (separados por comas)', f('genres')),
    h('label', {}, 'Serie', f('series')),
    h('label', {}, 'Número en la serie', h('input', { type: 'number', step: 'any', name: 'series_index', value: book.series_index ?? '' })),
    h('label', {}, 'Idioma', f('language')),
    h('label', {}, 'Año', f('published')),
    h('label', { class: 'full' }, 'Editorial', f('publisher')),
    h('label', { class: 'full' }, 'Descripción', h('textarea', { name: 'description', rows: 5 }, book.description ?? '')),
    h('div', { class: 'actions full' }, h('button', { class: 'btn primary' }, 'Guardar'), h('button', { class: 'btn', type: 'button', onclick: () => openDetail(book.id, view) }, 'Cancelar')),
  )
  dlg.replaceChildren(h('div', { class: 'sheet' }, h('header', {}, h('h2', {}, 'Editar libro'),
    h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))), form))
  if (!dlg.open) dlg.showModal()
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
  const visibility = h('select', { 'aria-label': 'Quién los ve', onchange: e => localStorage.setItem('libros.visibility', e.target.value) },
    ...[['privado', 'Solo yo'], ['compartido', 'Todos los usuarios']].map(([v, l]) =>
      h('option', { value: v, selected: (localStorage.getItem('libros.visibility') ?? 'privado') === v }, l)))
  function add(files) {
    for (const file of files) {
      const status = h('span', {}, 'En cola')
      const bar = h('i', { style: 'width:0' })
      queue.append(h('li', {}, h('div', { class: 'row' }, h('span', {}, file.name), status), h('div', { class: 'bar' }, bar)))
      // De uno en uno: el móvil y la conexión lo agradecen.
      chain = chain.then(() => uploadOne(file, status, bar, visibility.value)).then(ok => { if (ok) added++ })
    }
  }
  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Añadir libros'),
      h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => { dlg.close(); if (added) refresh(view) } }, icon('close'))),
    h('label', {}, 'Quién verá los libros que subas', visibility),
    drop, input, queue))
  dlg.onclose = () => { if (added) refresh(view); dlg.onclose = null }
  dlg.showModal()
}

async function uploadOne(file, status, bar, visibility) {
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
  form.append('meta', JSON.stringify({ ...meta, visibility }))
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

// Menú de la persona: su cuenta (contraseña, dispositivos), instalar la app
// y salir. La cuenta y la salida las sirve la pasarela dentro de la ruta de
// la app.
function userMenu() {
  const name = me.name || me.user || 'Mi cuenta'
  const menu = h('div', { class: 'menu', hidden: true },
    h('div', { class: 'who' }, h('strong', {}, name), me.user && me.user !== name ? h('span', { class: 'meta' }, me.user) : null),
    h('a', { href: '_cuenta' }, icon('user'), 'Mi cuenta y contraseña'),
    !installed() ? h('button', { type: 'button', onclick: () => { menu.hidden = true; installHelp() } }, icon('download'), 'Instalar la app') : null,
    h('form', { method: 'post', action: '_logout' }, h('button', {}, icon('back'), 'Salir')))
  const wrap = h('div', { class: 'usermenu' },
    h('button', { class: 'avatar', 'aria-label': `Cuenta de ${name}`, 'aria-haspopup': 'true',
      onclick: e => { e.stopPropagation(); menu.hidden = !menu.hidden } },
      h('span', { class: 'initial' }, name.trim()[0]?.toUpperCase() ?? '?'),
      h('span', { class: 'label' }, name)),
    menu)
  document.addEventListener('click', () => { menu.hidden = true })
  return wrap
}

// Tarjeta para instalar la app, si el navegador lo permite y no se ha
// descartado. Se vuelve a mostrar sola si el aviso llega después.
function installCard(view) {
  if (installed() || localStorage.getItem('libros.installDismissed')) return null
  const card = h('div', { class: 'install', hidden: !canInstall() },
    h('div', {}, h('strong', {}, 'Instala Libros como app'),
      h('p', { class: 'meta' }, 'Ábrela desde la pantalla de inicio, a pantalla completa y sin volver a entrar.')),
    h('button', { class: 'btn primary', onclick: () => install() }, icon('download'), 'Instalar'),
    h('button', { class: 'icon-btn', 'aria-label': 'No mostrar más', onclick: () => {
      localStorage.setItem('libros.installDismissed', '1')
      card.remove()
    } }, icon('close')))
  onInstallChange(() => { card.hidden = !canInstall() })
  return card
}

function debounce(fn, ms) {
  let t
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms) }
}
