// Explorar bibliotecas públicas legales (Project Gutenberg, Wikisource e
// Internet Archive con licencia libre) y añadir libros a la biblioteca. El
// servidor busca, descarga y guarda: el libro no pasa por el dispositivo.
import { h, icon, api, toast, hue, Offline } from './util.js'

const SOURCES = [
  ['todas', 'Todas', 'Busca a la vez en Project Gutenberg, Internet Archive y Wikisource.'],
  ['gutenberg', 'Project Gutenberg', 'Unos 75 000 clásicos en EPUB, con portadas. La mejor calidad.'],
  ['wikisource', 'Wikisource', 'Obras transcritas por voluntarios en muchos idiomas. Busca por título o autor.'],
  ['archive', 'Internet Archive', 'Solo documentos con licencia libre explícita; a veces son escaneos con OCR.'],
]
const LANGS = [['es', 'Español'], ['en', 'English'], ['fr', 'Français'], ['de', 'Deutsch'], ['it', 'Italiano'], ['pt', 'Português'], ['ca', 'Català'], ['', 'Todos los idiomas']]

const state = {
  source: localStorage.getItem('libros.explore.source') ?? 'todas',
  lang: localStorage.getItem('libros.explore.lang') ?? 'es',
  q: '',
  page: 1,
  items: [],
  more: false,
  loading: false,
}

export async function showExplore(view) {
  document.title = 'Explorar · Libros'
  state.page = 1
  render(view)
  await load(view)
}

async function load(view, append = false) {
  state.loading = true
  renderResults(view)
  const params = new URLSearchParams({ source: state.source, page: state.page })
  if (state.q) params.set('q', state.q)
  if (state.lang && !(state.source === 'wikisource' && !state.lang)) params.set('lang', state.lang)
  try {
    const res = await api(`api/catalog/search?${params}`)
    state.items = append ? [...state.items, ...res.items] : res.items
    state.more = res.more
    if (res.failed?.length) toast(`Sin respuesta de ${res.failed.map(f => SOURCES.find(([id]) => id === f)?.[1] ?? f).join(' y ')}; se muestran las demás`)
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión: explorar necesita internet' : e.message)
    if (!append) state.items = []
    state.more = false
  }
  state.loading = false
  renderResults(view)
}

function render(view) {
  const search = h('input', { type: 'search', placeholder: 'Título o autor…', value: state.q, 'aria-label': 'Buscar en la biblioteca pública',
    onkeydown: e => { if (e.key === 'Enter') { state.q = e.target.value.trim(); state.page = 1; load(view) } } })
  view.replaceChildren(h('div', { class: 'library' },
    h('div', { class: 'top' },
      h('a', { class: 'icon-btn', href: '#/', 'aria-label': 'Volver a la biblioteca' }, icon('back')),
      h('h1', {}, 'Explorar'),
      h('div', { class: 'search' }, icon('search'), search)),
    h('p', { class: 'meta legal' }, 'Bibliotecas públicas con obras de dominio público o licencia libre. Lo que añadas se descarga en tu biblioteca del servidor.'),
    h('div', { class: 'filters' },
      ...SOURCES.map(([id, name, hint]) => h('button', { class: 'chip', 'aria-pressed': String(state.source === id), title: hint, onclick: () => {
        state.source = id
        localStorage.setItem('libros.explore.source', id)
        state.page = 1
        render(view)
        load(view)
      } }, name)),
      h('select', { 'aria-label': 'Idioma', onchange: e => {
        state.lang = e.target.value
        localStorage.setItem('libros.explore.lang', state.lang)
        state.page = 1
        load(view)
      } }, ...LANGS.filter(([v]) => v || state.source !== 'wikisource').map(([v, l]) => h('option', { value: v, selected: state.lang === v }, l))),
      h('select', { 'aria-label': 'Quién verá lo que añadas', id: 'explore-visibility', onchange: e => localStorage.setItem('libros.visibility', e.target.value) },
        ...[['privado', 'Añadir solo para mí'], ['compartido', 'Añadir para todos']].map(([v, l]) =>
          h('option', { value: v, selected: (localStorage.getItem('libros.visibility') ?? 'privado') === v }, l))),
    ),
    h('p', { class: 'meta' }, SOURCES.find(([id]) => id === state.source)?.[2] ?? ''),
    h('div', { id: 'explore-results' }),
  ))
}

function renderResults(view) {
  const box = view.querySelector('#explore-results')
  if (!box) return
  if (state.loading && !state.items.length) {
    box.replaceChildren(h('div', { class: 'empty' }, 'Buscando…'))
    return
  }
  if (!state.items.length) {
    box.replaceChildren(h('div', { class: 'empty' }, state.source === 'wikisource' && !state.q
      ? 'Escribe un título o un autor y pulsa Intro para buscar en Wikisource.'
      : 'No hay resultados. Prueba con otro idioma o con menos palabras.'))
    return
  }
  box.replaceChildren(
    h('div', { class: 'grid' }, ...state.items.map(item => result(item))),
    state.more ? h('div', { class: 'more' }, h('button', { class: 'btn', disabled: state.loading, onclick: () => { state.page++; load(view, true) } },
      state.loading ? 'Cargando…' : 'Cargar más')) : null)
}

function result(item) {
  const c = h('div', { class: 'cover' },
    h('div', { class: 'ph', style: `--c1: hsl(${hue(item.title)} 30% 42%); --c2: hsl(${hue(item.title)} 35% 24%)` },
      h('b', {}, item.title), h('small', {}, item.authors?.[0] ?? '')))
  if (item.has_cover) {
    const img = h('img', { src: `api/catalog/cover?source=${encodeURIComponent(item.source)}&id=${encodeURIComponent(item.id)}`, alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  const action = h('div', { class: 'add' })
  const setAdded = bookId => action.replaceChildren(
    h('a', { class: 'btn small primary', href: `#/leer/${bookId}` }, 'Leer'),
    h('span', { class: 'meta' }, 'En tu biblioteca'))
  if (item.book_id) setAdded(item.book_id)
  else action.append(h('button', { class: 'btn small', onclick: async e => {
    const btn = e.currentTarget
    btn.disabled = true
    btn.textContent = 'Descargando…'
    try {
      const visibility = document.querySelector('#explore-visibility')?.value ?? 'privado'
      const res = await api('api/catalog/import', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source: item.source, id: item.id, visibility }) })
      item.book_id = res.book.id
      setAdded(res.book.id)
      toast(res.duplicate ? 'Ya estaba en tu biblioteca' : `Añadido: ${res.book.title}`)
    } catch (err) {
      toast(err.message)
      btn.disabled = false
      btn.textContent = 'Añadir'
    }
  } }, icon('plus'), 'Añadir'))
  const facts = [item.language?.toUpperCase(), item.year, item.downloads ? `${item.downloads.toLocaleString('es')} descargas` : ''].filter(Boolean)
  return h('div', { class: 'book result' },
    c,
    h('div', { class: 't', title: item.title }, item.title),
    h('div', { class: 'a' }, item.authors?.join(', ') || '—'),
    facts.length ? h('div', { class: 'meta small' }, facts.join(' · ')) : null,
    state.source === 'todas' ? h('div', { class: 'meta small src' }, SOURCES.find(([id]) => id === item.source)?.[1] ?? item.source) : null,
    action)
}
