// Series de cómic (manga, manhwa, manhua): las que sigues con sus capítulos
// sin leer, un buscador en las webs de origen y la ficha de cada serie. Los
// capítulos se abren en la web de origen; aquí se apunta por dónde vas.
import { h, icon, api, ago, toast, hue, Offline } from './util.js'

const SOURCES = { asura: 'Asura Scans', flame: 'Flame Comics' }
const num = n => Number(n).toString()

const state = {
  source: localStorage.getItem('libros.series.source') ?? 'todas',
  q: '',
  page: 1,
  items: [],
  more: false,
  loading: false,
  notice: '',
}

export async function showSeries(view, hash) {
  const m = hash.match(/^#\/series\/(\d+)/)
  if (m) return showDetail(view, Number(m[1]))
  if (hash.startsWith('#/series/buscar')) return showSearch(view)
  return showFollowed(view)
}

function header(title, back, tab) {
  return h('div', { class: 'top' },
    h('a', { class: 'icon-btn', href: back, 'aria-label': 'Volver' }, icon('back')),
    h('h1', {}, title),
    tab ? h('div', { class: 'filters tabs' },
      h('a', { class: 'chip', href: '#/series', 'aria-pressed': String(tab === 'siguiendo') }, 'Siguiendo'),
      h('a', { class: 'chip', href: '#/series/buscar', 'aria-pressed': String(tab === 'buscar') }, icon('search'), 'Buscar')) : null)
}

function cover(title, src, badge) {
  const c = h('div', { class: 'cover' },
    h('div', { class: 'ph', style: `--c1: hsl(${hue(title)} 30% 42%); --c2: hsl(${hue(title)} 35% 24%)` }, h('b', {}, title)))
  if (src) {
    const img = h('img', { src, alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  if (badge) c.append(h('span', { class: 'unread' }, badge))
  return c
}

// --- Siguiendo ---

async function showFollowed(view) {
  document.title = 'Series · Libros'
  view.replaceChildren(h('div', { class: 'library' }, header('Series', '#/', 'siguiendo'), h('div', { class: 'empty' }, 'Cargando…')))
  let list
  try {
    list = await api('api/series')
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión' : e.message)
    list = []
  }
  const body = list.length
    ? h('div', { class: 'grid' }, ...list.map(s => h('a', { class: 'book', href: `#/series/${s.id}` },
      cover(s.title, s.has_cover ? `api/series/${s.id}/portada` : null, s.unread && s.last_read != null ? `+${s.unread}` : null),
      h('div', { class: 't' }, s.title),
      h('div', { class: 'a' }, s.latest_number != null ? `Cap. ${num(s.latest_number)}${s.latest_at ? ' · ' + ago(s.latest_at) : ''}` : '—'),
      h('div', { class: 'meta small' }, s.last_read != null ? (s.unread ? `Vas por el ${num(s.last_read)}` : 'Al día') : 'Sin empezar'))))
    : h('div', { class: 'empty' },
      h('h2', {}, 'Aún no sigues ninguna serie'),
      h('p', {}, 'Busca tus manga y manhwa en Asura Scans y Flame Comics y síguelos para ver aquí los capítulos nuevos.'),
      h('a', { class: 'btn primary', href: '#/series/buscar' }, icon('search'), 'Buscar series'))
  view.replaceChildren(h('div', { class: 'library' }, header('Series', '#/', 'siguiendo'), body))
}

// --- Buscar ---

function showSearch(view) {
  document.title = 'Buscar series · Libros'
  state.page = 1
  const input = h('input', { type: 'search', placeholder: 'Título de la serie…', value: state.q, 'aria-label': 'Buscar series',
    onkeydown: e => { if (e.key === 'Enter') { state.q = e.target.value.trim(); state.page = 1; load(view) } } })
  view.replaceChildren(h('div', { class: 'library' },
    header('Series', '#/', 'buscar'),
    h('div', { class: 'filters' },
      h('div', { class: 'search' }, icon('search'), input),
      ...[['todas', 'Todas'], ['asura', SOURCES.asura], ['flame', SOURCES.flame]].map(([id, name]) =>
        h('button', { class: 'chip', 'aria-pressed': String(state.source === id), onclick: () => {
          state.source = id
          localStorage.setItem('libros.series.source', id)
          state.page = 1
          showSearch(view)
        } }, name))),
    h('div', { id: 'series-results' })))
  load(view)
}

async function load(view, append = false) {
  state.loading = true
  renderResults(view)
  const params = new URLSearchParams({ source: state.source, page: state.page })
  if (state.q) params.set('q', state.q)
  try {
    const res = await api(`api/series/buscar?${params}`)
    state.items = append ? [...state.items, ...res.items] : res.items
    state.more = res.more
    state.notice = res.failed?.length
      ? `Sin respuesta de ${res.failed.map(f => SOURCES[f] ?? f).join(' y ')}${res.notice ? ': ' + res.notice : '.'}`
      : ''
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión: buscar necesita internet' : e.message)
    if (!append) state.items = []
    state.more = false
  }
  state.loading = false
  renderResults(view)
}

function renderResults(view) {
  const box = view.querySelector('#series-results')
  if (!box) return
  const notice = state.notice && !state.loading ? h('p', { class: 'notice' }, state.notice) : ''
  if (state.loading && !state.items.length) return box.replaceChildren(h('div', { class: 'empty' }, 'Buscando…'))
  if (!state.items.length) return box.replaceChildren(notice, h('div', { class: 'empty' }, 'No hay resultados.'))
  box.replaceChildren(notice,
    h('div', { class: 'grid' }, ...state.items.map(result)),
    state.more ? h('div', { class: 'more' }, h('button', { class: 'btn', disabled: state.loading, onclick: () => { state.page++; load(view, true) } },
      state.loading ? 'Cargando…' : 'Cargar más')) : null)
}

function result(item) {
  const src = item.has_cover ? `api/series/portada?source=${encodeURIComponent(item.source)}&id=${encodeURIComponent(item.id)}` : null
  const action = item.followed
    ? h('a', { class: 'btn small', href: `#/series/${item.series_id}` }, icon('check'), 'Siguiendo')
    : h('button', { class: 'btn small primary', onclick: async e => {
      const btn = e.currentTarget
      btn.disabled = true
      btn.textContent = 'Siguiendo…'
      try {
        const res = await api('api/series', { method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ source: item.source, id: item.id }) })
        location.hash = `#/series/${res.series.id}`
      } catch (err) {
        toast(err.message)
        btn.disabled = false
        btn.textContent = 'Seguir'
      }
    } }, icon('plus'), 'Seguir')
  const facts = [item.kind, item.status, item.chapters ? `${item.chapters} caps.` : '', item.latest_at ? ago(item.latest_at) : ''].filter(Boolean)
  return h('div', { class: 'book result' },
    cover(item.title, src),
    h('div', { class: 't', title: item.title }, item.title),
    h('div', { class: 'meta small' }, [SOURCES[item.source], ...facts].join(' · ')),
    h('div', { class: 'add' }, action))
}

// --- Ficha de la serie ---

async function showDetail(view, id) {
  view.replaceChildren(h('div', { class: 'library' }, header('Serie', '#/series'), h('div', { class: 'empty' }, 'Cargando…')))
  let data
  try {
    data = await api(`api/series/${id}`)
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión' : e.message)
    return
  }
  renderDetail(view, data.series, data.chapters)
}

function renderDetail(view, s, chapters) {
  document.title = `${s.title} · Series`
  const source = SOURCES[s.source] ?? s.source
  const read = n => s.last_read != null && n <= s.last_read
  const setRead = async number => {
    try {
      const updated = await api(`api/series/${s.id}/leido`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ number }) })
      Object.assign(s, updated)
      renderDetail(view, s, chapters)
    } catch (e) { toast(e.message) }
  }
  // Al abrir un capítulo se apunta como leído (si va después de lo leído).
  const opened = ch => { if (s.followed && !read(ch.number)) setRead(ch.number) }
  const ordered = [...chapters].sort((a, b) => a.number - b.number)
  const previous = ch => { const i = ordered.findIndex(c => c.number === ch.number); return i > 0 ? ordered[i - 1].number : null }

  const progress = !s.followed ? null
    : s.last_read == null
      ? h('form', { class: 'inline progress', onsubmit: e => {
        e.preventDefault()
        const v = parseFloat(new FormData(e.target).get('n'))
        if (Number.isFinite(v) && v >= 0) setRead(v)
      } }, h('span', {}, '¿Ya ibas por algún capítulo?'),
        h('input', { type: 'number', name: 'n', step: 'any', min: 0, placeholder: 'Nº', 'aria-label': 'Último capítulo leído' }),
        h('button', { class: 'btn small' }, 'Guardar'))
      : h('p', { class: 'meta' }, s.unread ? `Vas por el capítulo ${num(s.last_read)} · ${s.unread} sin leer` : `Al día: capítulo ${num(s.last_read)}`)

  const rows = chapters.map(ch => {
    const early = ch.early_until && new Date(ch.early_until) > new Date()
    return h('li', { class: read(ch.number) ? 'read' : '' },
      h('a', { href: ch.url, target: '_blank', rel: 'noopener noreferrer', onclick: () => opened(ch) },
        h('span', { class: 'n' }, `Capítulo ${num(ch.number)}`),
        ch.title ? h('span', { class: 'ct' }, ch.title) : null,
        h('span', { class: 'meta small' }, [ch.published_at ? ago(ch.published_at) : '', early ? `acceso anticipado hasta ${new Date(ch.early_until).toLocaleString('es', { dateStyle: 'short', timeStyle: 'short' })}` : ''].filter(Boolean).join(' · '))),
      s.followed ? h('button', { class: 'icon-btn', 'aria-label': read(ch.number) ? 'Marcar como no leído' : 'Marcar leído hasta aquí',
        title: read(ch.number) ? 'Marcar como no leído' : 'Marcar leído hasta aquí',
        onclick: () => setRead(read(ch.number) ? previous(ch) : ch.number) }, icon('check')) : null)
  })

  view.replaceChildren(h('div', { class: 'library series-detail' },
    header('Series', '#/series'),
    h('div', { class: 'detail' },
      cover(s.title, s.has_cover ? `api/series/${s.id}/portada` : null),
      h('div', {},
        h('h3', {}, s.title),
        s.authors.length ? h('p', {}, s.authors.join(', ')) : null,
        h('p', { class: 'meta' }, [source, s.kind, s.status, s.latest_number != null ? `${chapters.length} capítulos` : ''].filter(Boolean).join(' · ')),
        s.genres.length ? h('p', { class: 'meta' }, s.genres.join(' · ')) : null,
        progress)),
    s.description ? h('details', { class: 'desc' }, h('summary', {}, 'Sinopsis'), s.description) : null,
    h('div', { class: 'actions' },
      s.followed && s.next ? h('a', { class: 'btn primary', href: s.next.url, target: '_blank', rel: 'noopener noreferrer', onclick: () => opened(s.next) },
        s.last_read == null ? `Empezar: capítulo ${num(s.next.number)}` : `Seguir: capítulo ${num(s.next.number)}`) : null,
      s.followed && s.unread ? h('button', { class: 'btn', onclick: () => setRead(s.latest_number) }, icon('check'), 'Marcar todo como leído') : null,
      s.followed
        ? h('button', { class: 'btn', onclick: async () => {
          if (!confirm(`¿Dejar de seguir «${s.title}»? Se borra lo que llevas leído.`)) return
          try { await api(`api/series/${s.id}`, { method: 'DELETE' }); toast('Ya no la sigues'); location.hash = '#/series' } catch (e) { toast(e.message) }
        } }, 'Dejar de seguir')
        : h('button', { class: 'btn primary', onclick: async () => {
          try {
            const res = await api('api/series', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source: s.source, id: s.source_id }) })
            renderDetail(view, res.series, res.chapters)
          } catch (e) { toast(e.message) }
        } }, icon('plus'), 'Seguir'),
      h('a', { class: 'btn', href: s.url, target: '_blank', rel: 'noopener noreferrer' }, `Abrir en ${source}`)),
    h('ul', { class: 'chapters' }, ...rows),
    h('p', { class: 'meta small' }, `Los capítulos se leen en la web de ${source}. Aquí se apunta por dónde vas y se avisa de los nuevos (se comprueban cada 30 minutos).`)))
}

// unreadTotal es el total de capítulos sin leer, para el aviso de la biblioteca.
export async function unreadTotal() {
  try {
    const list = await api('api/series')
    // Las series sin empezar no cuentan: todos sus capítulos estarían «sin leer».
    return list.filter(s => s.last_read != null).reduce((n, s) => n + s.unread, 0)
  } catch { return 0 }
}
