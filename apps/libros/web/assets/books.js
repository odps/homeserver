// Libros: la biblioteca separada por estados (en curso, por empezar, leídos,
// favoritos) con búsqueda, filtros y orden, y la ficha de cada libro a
// página completa. También la edición de datos y la subida de libros.
import { $, h, icon, api, ago, pct, formatSize, toast, hue, device, me, coverURL, shrinkCover, ambient, menu, pick, plural, Offline } from './util.js'
import { rememberLibrary, cachedLibrary, downloadedIds, isDownloaded, loadFile, removeDownload, localProgress } from './local.js'
import { prefs } from './prefs.js'

const TABS = [
  ['todos', 'Todos'],
  ['leyendo', 'En curso'],
  ['pendiente', 'Por empezar'],
  ['leido', 'Leídos'],
  ['favoritos', 'Favoritos'],
]
const SORTS = [['recent', 'Leídos recientemente'], ['added', 'Añadidos recientemente'], ['title', 'Título'], ['author', 'Autor']]
const ORIGINS = [['', 'Todos'], ['mine', 'Subidos por mí'], ['shared', 'Compartidos'], ['public', 'De bibliotecas públicas']]

const state = {
  tab: localStorage.getItem('libros.tab') ?? 'todos',
  q: '',
  sort: localStorage.getItem('libros.sort') ?? 'recent',
  formats: new Set(),
  langs: new Set(),
  genre: '',
  origin: '',
  showFilters: false,
  books: null,
}

let reshow = () => {}

export async function show(view, r) {
  reshow = () => show(view, r)
  if (r.id && /^\d+$/.test(r.id)) return showDetail(view, Number(r.id))
  if (r.id && TABS.some(([id]) => id === r.id)) state.tab = r.id
  return showList(view)
}

// --- Piezas que usan otras páginas ---

export function bookCover(b, { downloaded = false } = {}) {
  const c = h('div', { class: 'cover' },
    h('div', { class: 'ph', style: `--c1: hsl(${hue(b.title)} 30% 42%); --c2: hsl(${hue(b.title)} 35% 24%)` },
      h('b', {}, b.title), h('small', {}, b.authors?.[0] ?? '')))
  if (b.has_cover) {
    const img = h('img', { src: coverURL(b), alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  if (b.format && b.format !== 'epub') c.append(h('span', { class: 'tag' }, b.format))
  const corner = h('div', { class: 'corner' })
  if (b.favorite) corner.append(h('span', { class: 'heart', title: 'Favorito' }, icon('heart')))
  if (b.visibility === 'privado') corner.append(h('span', { title: 'Privado: solo lo ves tú' }, icon('lock')))
  if (downloaded) corner.append(h('span', { title: 'Guardado en este dispositivo' }, icon('download')))
  if (corner.children.length) c.append(corner)
  if (b.last && b.status !== 'leido') c.append(h('div', { class: 'progress', title: pct(b.last.fraction) }, h('i', { style: `width:${b.last.fraction * 100}%` })))
  return c
}

export function bookTile(b, downloaded) {
  const sub = b.status === 'leido' ? h('span', { class: 'status ok' }, icon('check'), 'Leído')
    : b.last ? h('span', { class: 'status idle' }, pct(b.last.fraction))
      : h('span', { class: 'a' }, b.authors.join(', ') || '—')
  return h('a', { class: 'tile', href: `#/libros/${b.id}` },
    bookCover(b, { downloaded: downloaded === true }),
    h('div', { class: 't' }, b.title),
    b.status === 'leido' || b.last ? h('div', { class: 'a' }, b.authors.join(', ') || '—') : null,
    sub)
}

// --- Lista ---

const byTab = (b, tab) => tab === 'todos' || (tab === 'favoritos' ? b.favorite : b.status === tab)

function matches(b) {
  const q = state.q.toLowerCase()
  if (q && ![b.title, ...b.authors, ...b.genres, b.series ?? '', b.isbn ?? '', b.publisher ?? ''].some(s => s.toLowerCase().includes(q))) return false
  if (state.formats.size && !state.formats.has(b.format)) return false
  if (state.langs.size && !state.langs.has(langOf(b))) return false
  if (state.genre && !b.genres.includes(state.genre)) return false
  if (state.origin === 'mine' && b.owner !== me.user) return false
  if (state.origin === 'shared' && b.visibility !== 'compartido') return false
  if (state.origin === 'public' && !b.source) return false
  return true
}

const langOf = b => (b.language ?? '').slice(0, 2).toLowerCase()

function sorted(list) {
  const key = {
    recent: b => -Date.parse(b.last?.updated_at ?? b.added_at),
    added: b => -Date.parse(b.added_at),
    title: b => b.title.toLowerCase(),
    author: b => (b.authors[0] ?? '~').toLowerCase() + b.title.toLowerCase(),
  }[state.sort] ?? (b => 0)
  return [...list].sort((a, b) => { const x = key(a), y = key(b); return x < y ? -1 : x > y ? 1 : 0 })
}

const activeFilters = () => state.formats.size + state.langs.size + (state.genre ? 1 : 0) + (state.origin ? 1 : 0)

async function showList(view) {
  document.title = 'Libros'
  if (!state.books) state.books = cachedLibrary()
  let offline = false
  const paint = downloaded => view.replaceChildren(renderList(view, downloaded, offline))
  paint(new Set())
  try {
    state.books = await api('api/books?sort=recent')
    rememberLibrary(state.books)
  } catch (e) {
    if (!(e instanceof Offline)) return toast(e.message)
    offline = true
  }
  paint(await downloadedIds())
}

function renderList(view, downloaded, offline) {
  const all = state.books ?? []
  const filtered = all.filter(matches)
  const counts = Object.fromEntries(TABS.map(([id]) => [id, filtered.filter(b => byTab(b, id)).length]))
  const list = sorted(filtered.filter(b => byTab(b, state.tab)))
  const rerender = () => view.replaceChildren(renderList(view, downloaded, offline))

  const search = h('input', { type: 'search', placeholder: 'Buscar por título, autor, género, ISBN…', value: state.q, 'aria-label': 'Buscar en tu biblioteca',
    oninput: debounce(e => { state.q = e.target.value.trim(); rerender(); $('.search input')?.focus() }, 250) })
  const nf = activeFilters()
  return h('div', { class: 'page' },
    h('div', { class: 'page-head' },
      h('h1', {}, 'Libros'),
      h('button', { class: 'btn primary', onclick: () => openUpload(), disabled: offline }, icon('plus'), h('span', { class: 'label' }, 'Añadir libros'))),
    h('div', { class: 'tabs', role: 'tablist' }, ...TABS.map(([id, label]) =>
      h('button', { role: 'tab', 'aria-selected': String(state.tab === id), onclick: () => {
        state.tab = id
        localStorage.setItem('libros.tab', id)
        history.replaceState(null, '', id === 'todos' ? '#/libros' : `#/libros/${id}`)
        rerender()
      } }, label, h('span', { class: 'n' }, counts[id])))),
    h('div', { class: 'toolbar' },
      h('div', { class: 'search' }, icon('search'), search),
      h('span', { class: 'grow' }),
      h('button', { class: 'chip', 'aria-pressed': String(state.showFilters), 'aria-expanded': String(state.showFilters),
        onclick: () => { state.showFilters = !state.showFilters; rerender() } }, icon('filter'), 'Filtros', nf ? h('span', { class: 'dot' }, nf) : null),
      pick('sparkle', 'Ordenar', state.sort, SORTS, v => { state.sort = v; localStorage.setItem('libros.sort', v); rerender() })),
    state.showFilters ? filtersPanel(all, rerender) : null,
    list.length
      ? h('div', { class: 'grid' }, ...list.map(b => bookTile(b, downloaded.has(b.id))))
      : emptyState(all.length))
}

function filtersPanel(all, rerender) {
  const formats = [...new Set(all.map(b => b.format))].sort()
  const langs = [...new Set(all.map(langOf).filter(Boolean))].sort()
  const genreCount = new Map()
  for (const b of all) for (const g of b.genres) genreCount.set(g, (genreCount.get(g) ?? 0) + 1)
  const genres = [...genreCount.entries()].sort((a, b) => b[1] - a[1]).slice(0, 40).map(([g]) => g)
  const toggle = (set, v) => () => { set.has(v) ? set.delete(v) : set.add(v); rerender() }
  const langName = code => new Intl.DisplayNames(['es'], { type: 'language' }).of(code) ?? code
  return h('div', { class: 'filters-panel' },
    formats.length > 1 ? h('div', { class: 'group' }, h('span', {}, 'Formato'),
      h('div', { class: 'chips' }, ...formats.map(f => h('button', { class: 'chip', 'aria-pressed': String(state.formats.has(f)), onclick: toggle(state.formats, f) }, f.toUpperCase())))) : null,
    langs.length > 1 ? h('div', { class: 'group' }, h('span', {}, 'Idioma'),
      h('div', { class: 'chips' }, ...langs.map(l => h('button', { class: 'chip', 'aria-pressed': String(state.langs.has(l)), onclick: toggle(state.langs, l) }, langName(l))))) : null,
    h('div', { class: 'group' }, h('span', {}, 'Procedencia'),
      h('div', { class: 'chips' }, ...ORIGINS.map(([v, l]) => h('button', { class: 'chip', 'aria-pressed': String(state.origin === v), onclick: () => { state.origin = v; rerender() } }, l)))),
    genres.length ? h('div', { class: 'group' }, h('span', {}, 'Género'),
      h('select', { onchange: e => { state.genre = e.target.value; rerender() } },
        h('option', { value: '' }, 'Todos los géneros'), ...genres.map(g => h('option', { value: g, selected: state.genre === g }, g)))) : null,
    h('footer', {},
      h('span', { class: 'meta' }, activeFilters() ? plural(activeFilters(), 'filtro activo', 'filtros activos') : 'Sin filtros'),
      h('button', { class: 'btn ghost small', onclick: () => {
        state.formats.clear(); state.langs.clear(); state.genre = ''; state.origin = ''; rerender()
      } }, 'Quitar filtros')))
}

function emptyState(hasBooks) {
  if (hasBooks) {
    const msg = { leyendo: 'No tienes libros en curso.', pendiente: 'No tienes libros por empezar.', leido: 'Aún no has terminado ningún libro.',
      favoritos: 'Marca libros con el corazón para tenerlos aquí.' }[state.tab]
    return h('div', { class: 'empty' }, icon('book'), h('h2', {}, state.q || activeFilters() ? 'Ningún libro coincide' : 'Nada por aquí'),
      h('p', {}, state.q || activeFilters() ? 'Prueba con otra búsqueda o quita algún filtro.' : msg ?? ''))
  }
  return h('div', { class: 'empty' }, icon('book'),
    h('h2', {}, 'Tu biblioteca está vacía'),
    h('p', {}, 'Sube tus libros (EPUB, PDF, MOBI, AZW3, FB2, CBZ) o busca clásicos en bibliotecas públicas.'),
    h('div', { class: 'actions' },
      h('button', { class: 'btn primary', onclick: () => openUpload() }, icon('plus'), 'Añadir libros'),
      h('a', { class: 'btn', href: '#/descubrir/libros' }, icon('compass'), 'Descubrir')))
}

// --- Ficha del libro ---

const STATUS = [['pendiente', 'Por empezar'], ['leyendo', 'En curso'], ['leido', 'Leído']]

async function showDetail(view, id) {
  let book, devices = [], offline = false
  try {
    ({ book, devices } = await api(`api/books/${id}`))
  } catch (e) {
    if (!(e instanceof Offline)) {
      view.replaceChildren(h('div', { class: 'page' }, backLink(), h('div', { class: 'empty' }, h('h2', {}, 'No se encuentra el libro'), h('p', {}, e.message))))
      return
    }
    offline = true
    book = cachedLibrary().find(b => b.id === id)
    if (!book) return view.replaceChildren(h('div', { class: 'page' }, backLink(), h('div', { class: 'empty' }, h('h2', {}, 'Sin conexión'))))
  }
  const local = await localProgress(id)
  const downloaded = await isDownloaded(id)
  view.replaceChildren(renderDetail(view, book, devices, { local, downloaded, offline }))
}

const backLink = () => h('a', { class: 'back', href: '#/libros', onclick: e => { if (history.length > 1) { e.preventDefault(); history.back() } } }, icon('back'), 'Libros')

function renderDetail(view, book, devices, { local, downloaded, offline }) {
  document.title = `${book.title} · Libros`
  const id = book.id
  const last = [book.last, local].filter(Boolean).sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))[0]
  const update = async (path, body, msg) => {
    try {
      const b = await api(`api/books/${id}${path}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (msg) toast(msg)
      Object.assign(book, b)
      view.replaceChildren(renderDetail(view, book, devices, { local, downloaded, offline }))
    } catch (e) { toast(e.message) }
  }
  const cover = bookCover(book, { downloaded })
  const facts = [book.format.toUpperCase(), formatSize(book.size), book.language, book.published, book.publisher, book.isbn && `ISBN ${book.isbn}`].filter(Boolean)
  const fav = h('button', { class: 'icon-btn fav', 'aria-pressed': String(book.favorite), 'aria-label': book.favorite ? 'Quitar de favoritos' : 'Añadir a favoritos',
    title: book.favorite ? 'Quitar de favoritos' : 'Añadir a favoritos', disabled: offline,
    onclick: () => update('/favorito', { favorite: !book.favorite }, book.favorite ? 'Quitado de favoritos' : 'Añadido a favoritos') }, icon('heart'))

  const hero = h('div', { class: 'detail-hero' },
    cover,
    h('div', {},
      h('div', { class: 'eyebrow' }, book.series ? `${book.series}${book.series_index != null ? ' · nº ' + book.series_index : ''}` : 'Libro'),
      h('h1', {}, book.title),
      h('p', { class: 'by' }, book.authors.join(', ') || 'Autor desconocido'),
      last && book.status !== 'leido' ? h('div', { class: 'progress-line' },
        h('div', { class: 'row' }, h('span', {}, `${pct(last.fraction)} leído`), h('span', { class: 'meta' }, `${ago(last.updated_at)} · ${last.device_id === device().id ? 'este dispositivo' : last.device_name}`)),
        h('div', { class: 'bar' }, h('i', { style: `width:${last.fraction * 100}%` }))) : null,
      h('div', { class: 'detail-actions' },
        h('a', { class: 'btn primary big', href: `#/leer/${id}` }, icon('play'), last ? 'Continuar' : book.status === 'leido' ? 'Releer' : 'Empezar a leer'),
        fav,
        h('div', { class: 'seg', role: 'group', 'aria-label': 'Estado' }, ...STATUS.map(([v, l]) =>
          h('button', { 'aria-pressed': String(book.status === v), disabled: offline, onclick: () => book.status !== v && update('/status', { status: v }, `Marcado como «${l.toLowerCase()}»`) }, l))),
        menu([
          book.can_edit && !offline ? ['edit', 'Editar datos y portada', () => openEdit(book)] : null,
          downloaded
            ? ['trash', 'Quitar de este dispositivo', async () => { await removeDownload(id); toast('Quitado de este dispositivo'); reshow() }]
            : ['download', 'Guardar para leer sin conexión', async () => {
              toast('Descargando…')
              try { await loadFile(book, { keep: true }); toast('Guardado para leer sin conexión'); reshow() } catch (e) { toast(e.message) }
            }],
          ['external', 'Descargar el fichero', `api/books/${id}/file`, 'download'],
          book.owner === me.user && !offline ? ['eye', book.visibility === 'privado' ? 'Compartir con todos' : 'Hacer privado', async () => {
            const visibility = book.visibility === 'privado' ? 'compartido' : 'privado'
            try {
              const b = await api(`api/books/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...editable(book), visibility }) })
              Object.assign(book, b)
              toast(visibility === 'privado' ? 'Ahora es privado' : 'Ahora lo ven todos')
              reshow()
            } catch (e) { toast(e.message) }
          }] : null,
          book.can_edit && !offline ? '-' : null,
          book.can_edit && !offline ? ['trash', 'Borrar de la biblioteca', async () => {
            if (!confirm(`¿Borrar «${book.title}» de la biblioteca? ${book.visibility === 'compartido' ? 'Desaparece para todos y se pierde el progreso de cada persona.' : 'Se borra también tu progreso.'}`)) return
            try {
              await api(`api/books/${id}`, { method: 'DELETE' })
              await removeDownload(id)
              toast('Libro borrado')
              location.hash = '#/libros'
            } catch (e) { toast(e.message) }
          }, 'danger'] : null,
        ])),
      h('div', { class: 'facts' }, ...facts.map(f => h('span', {}, f)))))
  const img = cover.querySelector('img')
  if (img) ambient(img, hero)

  const desc = book.description ? (() => {
    const p = h('div', { class: 'prose clamp' }, book.description)
    const more = h('button', { class: 'linkish', onclick: () => { p.classList.toggle('clamp'); more.textContent = p.classList.contains('clamp') ? 'Leer más' : 'Leer menos' } }, 'Leer más')
    return h('div', {}, h('h3', {}, 'Sinopsis'), p, book.description.length > 400 ? more : null)
  })() : h('p', { class: 'meta' }, book.can_edit ? 'Sin sinopsis. Puedes añadirla o buscarla en internet desde «Editar datos».' : 'Sin sinopsis.')

  const info = [
    ['Géneros', book.genres.join(', ')],
    ['Editorial', book.publisher], ['Año', book.published], ['Idioma', book.language], ['ISBN', book.isbn],
    ['Fichero', `${book.format.toUpperCase()} · ${formatSize(book.size)}`],
    ['Añadido', new Date(book.added_at).toLocaleDateString('es', { dateStyle: 'medium' })],
    ['Visibilidad', book.visibility === 'privado' ? 'Privado: solo lo ves tú' : `Compartido${book.owner === me.user ? ' por ti' : book.owner ? ` · subido por ${book.owner}` : ''}`],
    book.source ? ['Procedencia', book.source.split(':')[0].replace(/^./, c => c.toUpperCase())] : null,
    book.finished_at ? ['Terminado', new Date(book.finished_at).toLocaleDateString('es', { dateStyle: 'medium' })] : null,
  ].filter(x => x && x[1])

  return h('div', { class: 'page' },
    backLink(),
    hero,
    h('div', { class: 'detail-body' },
      h('div', {}, desc),
      h('aside', {},
        h('div', { class: 'card' }, h('h3', {}, 'Detalles'), h('dl', { class: 'kv' }, ...info.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)]))),
        devices.length ? h('div', { class: 'card' }, h('h3', {}, 'Progreso por dispositivo'),
          h('ul', { class: 'devices' }, ...devices.map(d => h('li', {},
            h('span', {}, d.device_id === device().id ? `${d.device_name} (este)` : d.device_name),
            h('span', {}, pct(d.fraction)),
            h('span', { class: 'meta' }, ago(d.updated_at)))))) : null)))
}

const editable = b => ({
  title: b.title, authors: b.authors, genres: b.genres, series: b.series ?? '', series_index: b.series_index,
  language: b.language ?? '', publisher: b.publisher ?? '', published: b.published ?? '', description: b.description ?? '',
  isbn: b.isbn ?? '',
})

function openEdit(book) {
  const dlg = $('#dialog')
  const f = name => h('input', { type: 'text', name, value: (Array.isArray(book[name]) ? book[name].join(', ') : book[name]) ?? '' })
  let newCover = null   // imagen elegida a mano
  let coverQuery = null // portada encontrada que se usará al guardar («isbn=…» o «ref=…»)
  const preview = bookCover(book)
  const coverInput = h('input', { type: 'file', accept: 'image/*', hidden: true, onchange: async e => {
    const file = e.target.files[0]
    if (!file) return
    newCover = await shrinkCover(file)
    coverQuery = null
    showCover(URL.createObjectURL(newCover))
  } })
  const showCover = src => {
    preview.querySelector('img')?.remove()
    preview.querySelector('.fmt.new')?.remove()
    preview.append(h('img', { src, alt: '' }))
  }
  const found = h('div', { class: 'found', hidden: true })
  const isbnInput = h('input', { type: 'text', name: 'isbn', inputmode: 'numeric', value: book.isbn ?? '', placeholder: '978…' })
  // Busca con lo que haya en el formulario: por ISBN (si lo hay) y por título
  // y autor a la vez, y enseña todos los resultados para elegir.
  const searchWeb = async e => {
    const btn = e.currentTarget
    const title = form.elements.title.value.trim()
    const author = form.elements.authors.value.split(',')[0].trim()
    const isbn = isbnInput.value.trim()
    if (!title && !isbn) return toast('Escribe el título o el ISBN')
    btn.disabled = true
    found.hidden = false
    found.replaceChildren(h('p', { class: 'meta' }, 'Buscando…'))
    try {
      const [byISBN, byTitle] = await Promise.allSettled([
        isbn ? api(`api/isbn/${encodeURIComponent(isbn)}`) : Promise.resolve(null),
        title ? api(`api/meta/buscar?${new URLSearchParams({ title, author })}`) : Promise.resolve({ items: [] }),
      ])
      if (byISBN.status === 'rejected' && byTitle.status === 'rejected') throw byTitle.reason
      const items = [...(byISBN.value ? [byISBN.value] : []), ...(byTitle.value?.items ?? [])]
      if (!items.length) found.replaceChildren(h('p', { class: 'meta' }, 'Sin resultados. Prueba con el título original, sin el autor o con el ISBN.'),
        h('button', { type: 'button', class: 'btn small', onclick: () => { found.hidden = true } }, 'Cerrar'))
      else found.replaceChildren(h('strong', {}, 'Elige el que corresponde'),
        h('ul', { class: 'results' }, ...items.map(it => h('li', {},
          h('button', { type: 'button', class: 'result', onclick: () => showFound(it, it.ref ? `ref=${it.ref}` : null) },
            it.ref ? h('img', { src: `api/meta/portada?ref=${it.ref}`, alt: '', loading: 'lazy' }) : h('span', { class: 'nocover' }, icon('book')),
            h('span', {}, h('strong', {}, it.title), h('span', { class: 'meta' }, [it.authors.join(', '), it.published, it.publisher].filter(Boolean).join(' · ')))))),
        h('button', { type: 'button', class: 'btn small', onclick: () => { found.hidden = true } }, 'Cerrar'))
    } catch (err) {
      found.hidden = true
      toast(err.message)
    }
    btn.disabled = false
  }
  function showFound(info, cover) {
    const useCover = h('input', { type: 'checkbox', checked: info.has_cover && !!cover })
    const rows = [['Título', info.title], ['Autores', info.authors?.join(', ')], ['Editorial', info.publisher], ['Año', info.published],
      ['ISBN', info.isbn], ['Géneros', info.genres?.join(', ')], ['Idioma', info.language]].filter(([, v]) => v)
    found.hidden = false
    found.replaceChildren(
      h('strong', {}, 'Datos encontrados'),
      h('dl', {}, ...rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
      info.has_cover && cover ? h('label', { class: 'check' }, useCover, 'Usar también su portada') : null,
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'btn small primary', onclick: () => {
          const set = (name, v) => { if (v) form.elements[name].value = Array.isArray(v) ? v.join(', ') : v }
          set('title', info.title); set('authors', info.authors); set('publisher', info.publisher); set('published', info.published)
          set('genres', info.genres); set('language', info.language); set('description', info.description); set('isbn', info.isbn)
          if (info.has_cover && cover && useCover.checked) {
            coverQuery = cover
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
      else if (coverQuery) await api(`api/books/${book.id}/cover?${coverQuery}`, { method: 'PUT' })
      toast('Guardado')
      dlg.close()
      reshow()
    } catch (err) {
      toast(err.message)
      if (btn) btn.disabled = false
    }
  } },
    h('div', { class: 'full cover-edit' }, preview,
      h('div', {},
        h('button', { type: 'button', class: 'btn small', onclick: () => coverInput.click() }, icon('edit'), 'Cambiar portada'),
        h('p', { class: 'meta' }, 'Una imagen de tu dispositivo, o la de los datos que encuentres en internet.')),
      coverInput),
    h('label', { class: 'full' }, 'Título', f('title')),
    h('label', { class: 'full' }, 'Autores (separados por comas)', f('authors')),
    h('label', { class: 'full' }, 'ISBN (opcional)', isbnInput),
    h('div', { class: 'full' }, h('button', { type: 'button', class: 'btn small', onclick: searchWeb }, icon('search'), 'Buscar datos en internet'),
      h('p', { class: 'meta' }, 'Con el título, el autor y el ISBN que haya escritos; eliges el resultado que corresponde.')),
    found,
    h('label', { class: 'full' }, 'Géneros (separados por comas)', f('genres')),
    h('label', {}, 'Serie', f('series')),
    h('label', {}, 'Número en la serie', h('input', { type: 'number', step: 'any', name: 'series_index', value: book.series_index ?? '' })),
    h('label', {}, 'Idioma', f('language')),
    h('label', {}, 'Año', f('published')),
    h('label', { class: 'full' }, 'Editorial', f('publisher')),
    h('label', { class: 'full' }, 'Descripción', h('textarea', { name: 'description', rows: 5 }, book.description ?? '')),
    h('div', { class: 'actions full' }, h('button', { class: 'btn primary' }, 'Guardar'), h('button', { class: 'btn', type: 'button', onclick: () => dlg.close() }, 'Cancelar')),
  )
  dlg.replaceChildren(h('div', { class: 'sheet' }, h('header', {}, h('h2', {}, 'Editar libro'),
    h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))), form))
  if (!dlg.open) dlg.showModal()
}

// Subida: metadatos y portada se extraen aquí con foliate-js.
const accept = '.epub,.pdf,.mobi,.azw3,.azw,.fb2,.fbz,.cbz'
function openUpload() {
  const dlg = $('#dialog')
  const queue = h('ul', { class: 'queue' })
  const input = h('input', { type: 'file', accept, multiple: true, hidden: true, onchange: e => add(e.target.files) })
  const drop = h('div', { class: 'drop', role: 'button', tabindex: 0, onclick: () => input.click(),
    ondragover: e => { e.preventDefault(); drop.classList.add('over') },
    ondragleave: () => drop.classList.remove('over'),
    ondrop: e => { e.preventDefault(); drop.classList.remove('over'); add(e.dataTransfer.files) } },
    h('p', {}, h('strong', {}, 'Arrastra tus libros aquí'), h('br'), 'o pulsa para elegirlos'),
    h('p', { class: 'meta' }, 'EPUB, PDF, MOBI, AZW3, FB2, FBZ y CBZ · hasta 250 MB cada uno'))
  let added = 0
  let chain = Promise.resolve()
  const visibility = h('select', { 'aria-label': 'Quién los ve' },
    ...[['privado', 'Solo yo'], ['compartido', 'Todos los usuarios']].map(([v, l]) =>
      h('option', { value: v, selected: prefs().visibility === v }, l)))
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
      h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => { dlg.close(); if (added) reshow() } }, icon('close'))),
    h('label', {}, 'Quién verá los libros que subas', visibility),
    drop, input, queue))
  dlg.onclose = () => { if (added) reshow(); dlg.onclose = null }
  dlg.showModal()
}

async function uploadOne(file, status, bar, visibility) {
  if (file.size > 250 * 1024 * 1024) {
    status.textContent = 'Supera 250 MB'
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


function debounce(fn, ms) {
  let t
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms) }
}
