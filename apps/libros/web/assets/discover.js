// Descubrir: buscar libros en bibliotecas públicas legales (Project
// Gutenberg, Wikisource, Internet Archive con licencia libre) y series de
// cómic en Asura Scans y Flame Comics. Los resultados se conservan al ir a una
// ficha y volver; seguir una serie o añadir un libro no te saca de la lista.
import { $, h, icon, api, toast, hue, Offline } from './util.js'
import { prefs } from './prefs.js'
import { seriesCover, SOURCES as COMIC_SOURCES } from './comics.js'

const BOOK_SOURCES = [
  ['todas', 'Todas', 'Busca a la vez en Project Gutenberg, Internet Archive y Wikisource.'],
  ['gutenberg', 'Project Gutenberg', 'Unos 75 000 clásicos en EPUB, con portadas. La mejor calidad.'],
  ['wikisource', 'Wikisource', 'Obras transcritas por voluntarios. Busca por título o autor.'],
  ['archive', 'Internet Archive', 'Solo documentos con licencia libre explícita; a veces son escaneos con OCR.'],
]
export const LANGS = [['es', 'Español'], ['en', 'English'], ['fr', 'Français'], ['de', 'Deutsch'], ['it', 'Italiano'], ['pt', 'Português'], ['ca', 'Català'], ['', 'Todos los idiomas']]
const COMIC_SORTS = [['popular', 'Populares'], ['updated', 'Actualizadas'], ['rating', 'Mejor valoradas'], ['title', 'A–Z']]
const COMIC_STATUS = [['', 'Cualquier estado'], ['ongoing', 'En emisión'], ['completed', 'Terminadas'], ['hiatus', 'En pausa'], ['dropped', 'Abandonadas']]

const books = { source: 'todas', lang: null, q: '', page: 1, items: null, more: false, loading: false, key: '' }
const comics = { source: null, q: '', sort: 'popular', status: '', page: 1, items: null, more: false, loading: false, notice: '', key: '' }
let tab = localStorage.getItem('libros.discover') ?? 'libros'

export async function show(view, r) {
  if (r.id === 'libros' || r.id === 'comics') tab = r.id
  localStorage.setItem('libros.discover', tab)
  books.lang ??= prefs().search_lang
  comics.source ??= prefs().comic_source
  document.title = 'Descubrir · Libros'
  render(view)
  const st = tab === 'libros' ? books : comics
  // Volver desde una ficha: los resultados ya están, no se vuelve a buscar.
  if (st.items === null || st.key !== keyOf()) await load(view)
}

const keyOf = () => tab === 'libros'
  ? JSON.stringify([books.source, books.lang, books.q])
  : JSON.stringify([comics.source, comics.q, comics.sort, comics.status])

function render(view) {
  const st = tab === 'libros' ? books : comics
  const setTab = t => { tab = t; history.replaceState(null, '', `#/descubrir/${t}`); show(view, { id: t }) }
  const search = h('input', { type: 'search', value: st.q, 'aria-label': 'Buscar',
    placeholder: tab === 'libros' ? 'Título o autor…' : 'Título de la serie…',
    onkeydown: e => { if (e.key === 'Enter') { st.q = e.target.value.trim(); reload(view) } } })
  view.replaceChildren(h('div', { class: 'page' },
    h('div', { class: 'page-head' },
      h('h1', {}, 'Descubrir'),
      h('div', { class: 'seg', role: 'tablist' },
        h('button', { role: 'tab', 'aria-pressed': String(tab === 'libros'), onclick: () => setTab('libros') }, 'Libros'),
        h('button', { role: 'tab', 'aria-pressed': String(tab === 'comics'), onclick: () => setTab('comics') }, 'Cómics'))),
    h('p', { class: 'lede' }, tab === 'libros'
      ? 'Obras de dominio público o con licencia libre. Lo que añadas se guarda en tu biblioteca.'
      : 'Sigue series de Asura Scans y Flame Comics para ver aquí cada capítulo nuevo. Los capítulos se leen en su web.'),
    h('div', { class: 'toolbar searchbar' },
      h('div', { class: 'search' }, icon('search'), search),
      h('button', { class: 'btn', onclick: () => { st.q = search.value.trim(); reload(view) } }, 'Buscar')),
    tab === 'libros' ? bookFilters(view) : comicFilters(view),
    h('div', { id: 'results' })))
  renderResults(view)
}

function reload(view) {
  const st = tab === 'libros' ? books : comics
  st.page = 1
  st.items = null
  load(view)
}

function bookFilters(view) {
  return h('div', { class: 'toolbar' },
    h('div', { class: 'chips' }, ...BOOK_SOURCES.map(([id, name, hint]) =>
      h('button', { class: 'chip', title: hint, 'aria-pressed': String(books.source === id), onclick: () => { books.source = id; render(view); reload(view) } }, name))),
    h('span', { class: 'grow' }),
    h('select', { 'aria-label': 'Idioma', onchange: e => { books.lang = e.target.value; reload(view) } },
      ...LANGS.filter(([v]) => v || books.source !== 'wikisource').map(([v, l]) => h('option', { value: v, selected: books.lang === v }, l))),
    h('select', { 'aria-label': 'Quién verá lo que añadas', id: 'add-visibility' },
      ...[['privado', 'Añadir solo para mí'], ['compartido', 'Añadir para todos']].map(([v, l]) =>
        h('option', { value: v, selected: prefs().visibility === v }, l))))
}

function comicFilters(view) {
  return h('div', { class: 'toolbar' },
    h('div', { class: 'chips' }, ...[['todas', 'Todas'], ['asura', COMIC_SOURCES.asura], ['flame', COMIC_SOURCES.flame]].map(([id, name]) =>
      h('button', { class: 'chip', 'aria-pressed': String(comics.source === id), onclick: () => { comics.source = id; render(view); reload(view) } }, name))),
    h('span', { class: 'grow' }),
    h('select', { 'aria-label': 'Ordenar', onchange: e => { comics.sort = e.target.value; reload(view) } },
      ...COMIC_SORTS.map(([v, l]) => h('option', { value: v, selected: comics.sort === v }, l))),
    h('select', { 'aria-label': 'Estado', onchange: e => { comics.status = e.target.value; reload(view) } },
      ...COMIC_STATUS.map(([v, l]) => h('option', { value: v, selected: comics.status === v }, l))))
}

async function load(view, append = false) {
  const st = tab === 'libros' ? books : comics
  const which = tab
  st.loading = true
  renderResults(view)
  let url
  if (which === 'libros') {
    const p = new URLSearchParams({ source: books.source, page: books.page })
    if (books.q) p.set('q', books.q)
    if (books.lang) p.set('lang', books.lang)
    url = `api/catalog/search?${p}`
  } else {
    const p = new URLSearchParams({ source: comics.source, page: comics.page, sort: comics.sort })
    if (comics.q) p.set('q', comics.q)
    if (comics.status) p.set('status', comics.status)
    url = `api/series/buscar?${p}`
  }
  try {
    const res = await api(url)
    // Sin duplicados al cargar más (por si la fuente repite resultados).
    const seen = new Set((append ? st.items ?? [] : []).map(i => i.source + ':' + i.id))
    const fresh = res.items.filter(i => !seen.has(i.source + ':' + i.id) && seen.add(i.source + ':' + i.id))
    st.items = append ? [...(st.items ?? []), ...fresh] : fresh
    st.more = res.more && (fresh.length > 0 || !append)
    st.key = keyOf()
    const names = which === 'libros' ? Object.fromEntries(BOOK_SOURCES.map(([id, n]) => [id, n])) : COMIC_SOURCES
    st.notice = res.failed?.length ? `Sin respuesta de ${res.failed.map(f => names[f] ?? f).join(' y ')}${res.notice ? ': ' + res.notice : '.'}` : ''
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión: descubrir necesita internet' : e.message)
    if (!append) st.items = []
    st.more = false
  }
  st.loading = false
  if (tab === which) renderResults(view)
}

function renderResults(view) {
  const box = view.querySelector('#results')
  if (!box) return
  const st = tab === 'libros' ? books : comics
  const notice = st.notice && !st.loading ? h('p', { class: 'notice' }, st.notice) : ''
  if (st.loading && !st.items?.length) {
    return box.replaceChildren(h('div', { class: 'grid skeleton' }, ...Array.from({ length: 10 }, () =>
      h('div', { class: 'tile' }, h('div', { class: 'cover' }), h('div', { class: 't' }), h('div', { class: 'a' })))))
  }
  if (!st.items?.length) {
    return box.replaceChildren(notice, h('div', { class: 'empty' }, h('h2', {}, 'Sin resultados'),
      h('p', {}, tab === 'comics' || books.source !== 'wikisource' || books.q ? 'Prueba con otras palabras, otro idioma u otra fuente.' : 'Escribe un título o un autor para buscar en Wikisource.')))
  }
  box.replaceChildren(notice,
    h('div', { class: 'grid' }, ...st.items.map(tab === 'libros' ? bookResult : comicResult)),
    st.more ? h('div', { class: 'more' }, h('button', { class: 'btn', disabled: st.loading, onclick: () => { st.page++; load(view, true) } },
      st.loading ? 'Cargando…' : 'Cargar más')) : '')
}

// --- Resultados ---

function bookResult(item) {
  const c = h('div', { class: 'cover' },
    h('div', { class: 'ph', style: `--c1: hsl(${hue(item.title)} 30% 42%); --c2: hsl(${hue(item.title)} 35% 24%)` },
      h('b', {}, item.title), h('small', {}, item.authors?.[0] ?? '')))
  if (item.has_cover) {
    const img = h('img', { src: `api/catalog/cover?source=${encodeURIComponent(item.source)}&id=${encodeURIComponent(item.id)}`, alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  const action = h('div', { class: 'actions' })
  const added = bookId => action.replaceChildren(h('a', { class: 'btn small', href: `#/libros/${bookId}` }, icon('check'), 'En tu biblioteca'))
  if (item.book_id) added(item.book_id)
  else action.append(h('button', { class: 'btn small primary', onclick: async e => {
    const btn = e.currentTarget
    btn.disabled = true
    btn.textContent = 'Añadiendo…'
    try {
      const visibility = $('#add-visibility')?.value ?? prefs().visibility
      const res = await api('api/catalog/import', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source: item.source, id: item.id, visibility }) })
      item.book_id = res.book.id
      added(res.book.id)
      toast(res.duplicate ? 'Ya estaba en tu biblioteca' : `Añadido: ${res.book.title}`)
    } catch (err) {
      toast(err.message)
      btn.disabled = false
      btn.replaceChildren(icon('plus'), 'Añadir')
    }
  } }, icon('plus'), 'Añadir'))
  const facts = [item.language?.toUpperCase(), item.year, books.source === 'todas' ? BOOK_SOURCES.find(([id]) => id === item.source)?.[1] : ''].filter(Boolean)
  return h('div', { class: 'tile' },
    c,
    h('div', { class: 't', title: item.title }, item.title),
    h('div', { class: 'a' }, item.authors?.join(', ') || '—'),
    facts.length ? h('div', { class: 'meta small' }, facts.join(' · ')) : null,
    action)
}

function comicResult(item) {
  const src = item.has_cover ? `api/series/portada?source=${encodeURIComponent(item.source)}&id=${encodeURIComponent(item.id)}` : null
  const tile = h('div', { class: 'tile' })
  const paint = () => {
    const cover = seriesCover(item.title, src, item.followed ? h('span', { class: 'pill ok' }, icon('check'), 'Siguiendo') : null)
    const head = item.followed && item.series_id ? h('a', { href: `#/comics/${item.series_id}`, style: 'color:inherit;text-decoration:none;display:grid;gap:.5rem' }, cover, h('div', { class: 't' }, item.title)) : null
    const facts = [comics.source === 'todas' ? COMIC_SOURCES[item.source] : '', item.kind, item.status, item.chapters ? `${item.chapters} caps.` : ''].filter(Boolean)
    tile.replaceChildren(
      head ?? cover, head ? '' : h('div', { class: 't', title: item.title }, item.title),
      h('div', { class: 'meta small' }, facts.join(' · ')),
      h('div', { class: 'actions' }, item.followed
        ? h('a', { class: 'btn small', href: `#/comics/${item.series_id}` }, 'Ver capítulos')
        : h('button', { class: 'btn small primary', onclick: async e => {
          const btn = e.currentTarget
          btn.disabled = true
          btn.textContent = 'Siguiendo…'
          try {
            const res = await api('api/series', { method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ source: item.source, id: item.id }) })
            item.followed = true
            item.series_id = res.series.id
            window.dispatchEvent(new CustomEvent('libros:series'))
            toast(`Sigues «${item.title}»`)
            paint()
          } catch (err) {
            toast(err.message)
            btn.disabled = false
            btn.replaceChildren(icon('plus'), 'Seguir')
          }
        } }, icon('plus'), 'Seguir')))
  }
  paint()
  return tile
}
