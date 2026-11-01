// Cómics: las series que sigues separadas por estado (con capítulos nuevos,
// al día, por empezar, favoritas) y la ficha de cada serie con sus capítulos.
// Los capítulos se leen en la web de origen; aquí se apunta por dónde vas.
import { $, h, icon, api, ago, toast, hue, ambient, menu, pick, plural, Offline } from './util.js'

export const SOURCES = { asura: 'Asura Scans', flame: 'Flame Comics' }
const num = n => Number(n).toString()

const TABS = [
  ['todas', 'Todas'],
  ['nuevos', 'Con nuevos'],
  ['aldia', 'Al día'],
  ['sinempezar', 'Por empezar'],
  ['favoritas', 'Favoritas'],
]
const SORTS = [['nuevos', 'Capítulos nuevos primero'], ['actualizadas', 'Actualizadas recientemente'], ['titulo', 'Título']]

const state = {
  tab: localStorage.getItem('libros.comics.tab') ?? 'todas',
  sort: localStorage.getItem('libros.comics.sort') ?? 'nuevos',
  source: '',
  q: '',
  list: null,
}

// Estado de una serie para quien la sigue.
export function phase(s) {
  if (s.last_read == null) return 'sinempezar'
  return s.unread > 0 ? 'nuevos' : 'aldia'
}
const finished = s => /complet|finish|termin|end/i.test(s.status ?? '')

export async function show(view, r) {
  if (r.id && /^\d+$/.test(r.id)) return showDetail(view, Number(r.id))
  if (r.id && TABS.some(([id]) => id === r.id)) state.tab = r.id
  return showList(view)
}

// --- Piezas que usan otras páginas ---

export function seriesCover(title, src, pill) {
  const c = h('div', { class: 'cover' },
    h('div', { class: 'ph', style: `--c1: hsl(${hue(title)} 30% 42%); --c2: hsl(${hue(title)} 35% 24%)` }, h('b', {}, title)))
  if (src) {
    const img = h('img', { src, alt: '', loading: 'lazy', onerror: () => img.remove() })
    c.append(img)
  }
  if (pill) c.append(pill)
  return c
}

function statusPill(s) {
  const p = phase(s)
  if (p === 'nuevos') return h('span', { class: 'pill new', title: plural(s.unread, 'capítulo nuevo', 'capítulos nuevos') }, `+${s.unread}`)
  if (p === 'aldia') return h('span', { class: 'pill ok' }, icon('check'), 'Al día')
  return h('span', { class: 'pill idle' }, 'Sin empezar')
}

export function seriesTile(s) {
  const cover = seriesCover(s.title, s.has_cover ? `api/series/${s.id}/portada` : null, statusPill(s))
  if (s.favorite) cover.append(h('div', { class: 'corner' }, h('span', { class: 'heart', title: 'Favorita' }, icon('heart'))))
  const latest = s.latest_number != null ? num(s.latest_number) : null
  const read = s.last_read != null && s.latest_number ? Math.min(1, s.last_read / s.latest_number) : 0
  return h('a', { class: 'tile', href: `#/comics/${s.id}` },
    cover,
    h('div', { class: 't' }, s.title),
    s.last_read != null
      ? h('div', { class: 'a' }, `Cap. ${num(s.last_read)} de ${latest ?? '?'}${finished(s) ? ' · terminada' : ''}`)
      : h('div', { class: 'a' }, latest ? `${latest} capítulos${finished(s) ? ' · terminada' : ''}` : '—'),
    s.last_read != null ? h('div', { class: 'bar' }, h('i', { style: `width:${read * 100}%` })) : null)
}

// --- Lista ---

const byTab = (s, tab) => tab === 'todas' || (tab === 'favoritas' ? s.favorite : phase(s) === tab)

function sorted(list) {
  const t = s => Date.parse(s.latest_at ?? 0) || 0
  const by = {
    nuevos: (a, b) => (phase(a) === 'nuevos') !== (phase(b) === 'nuevos') ? (phase(a) === 'nuevos' ? -1 : 1) : t(b) - t(a),
    actualizadas: (a, b) => t(b) - t(a),
    titulo: (a, b) => a.title.localeCompare(b.title, 'es'),
  }[state.sort]
  return [...list].sort(by)
}

async function showList(view) {
  document.title = 'Cómics · Libros'
  const paint = () => view.replaceChildren(renderList(view))
  if (state.list) paint()
  else view.replaceChildren(h('div', { class: 'page' }, head(), skeleton()))
  try {
    state.list = await api('api/series')
    window.dispatchEvent(new CustomEvent('libros:series', { detail: state.list }))
  } catch (e) {
    toast(e instanceof Offline ? 'Sin conexión: se muestra la última lista' : e.message)
    state.list ??= []
  }
  paint()
}

const head = () => h('div', { class: 'page-head' },
  h('h1', {}, 'Cómics'),
  h('a', { class: 'btn primary', href: '#/descubrir/comics' }, icon('search'), h('span', { class: 'label' }, 'Buscar series')))

const skeleton = () => h('div', { class: 'grid skeleton' }, ...Array.from({ length: 8 }, () => h('div', { class: 'tile' }, h('div', { class: 'cover' }), h('div', { class: 't' }), h('div', { class: 'a' }))))

function renderList(view) {
  const all = state.list ?? []
  const rerender = () => view.replaceChildren(renderList(view))
  const q = state.q.toLowerCase()
  const filtered = all.filter(s => (!state.source || s.source === state.source) && (!q || s.title.toLowerCase().includes(q)))
  const counts = Object.fromEntries(TABS.map(([id]) => [id, filtered.filter(s => byTab(s, id)).length]))
  const list = sorted(filtered.filter(s => byTab(s, state.tab)))
  const unread = filtered.filter(s => phase(s) === 'nuevos').reduce((n, s) => n + s.unread, 0)
  const sources = [...new Set(all.map(s => s.source))]

  if (!all.length) {
    return h('div', { class: 'page' }, head(), h('div', { class: 'empty' }, icon('layers'),
      h('h2', {}, 'Aún no sigues ninguna serie'),
      h('p', {}, 'Busca tus manga y manhwa en Asura Scans y Flame Comics. Te avisaremos aquí de cada capítulo nuevo.'),
      h('a', { class: 'btn primary', href: '#/descubrir/comics' }, icon('search'), 'Buscar series')))
  }
  return h('div', { class: 'page' },
    head(),
    unread ? h('p', { class: 'lede' }, `${plural(unread, 'capítulo nuevo', 'capítulos nuevos')} por leer en ${plural(counts.nuevos, 'serie', 'series')}.`) : null,
    h('div', { class: 'tabs', role: 'tablist' }, ...TABS.map(([id, label]) =>
      h('button', { role: 'tab', 'aria-selected': String(state.tab === id), onclick: () => {
        state.tab = id
        localStorage.setItem('libros.comics.tab', id)
        history.replaceState(null, '', id === 'todas' ? '#/comics' : `#/comics/${id}`)
        rerender()
      } }, label, h('span', { class: `n${id === 'nuevos' && counts[id] ? ' hot' : ''}` }, counts[id])))),
    h('div', { class: 'toolbar' },
      h('div', { class: 'search' }, icon('search'), h('input', { type: 'search', placeholder: 'Buscar entre tus series…', value: state.q, 'aria-label': 'Buscar entre tus series',
        oninput: e => { state.q = e.target.value.trim(); rerender(); $('.search input')?.focus() } })),
      h('span', { class: 'grow' }),
      sources.length > 1 ? pick('layers', 'Web', state.source, [['', 'Todas las webs'], ...sources.map(s => [s, SOURCES[s] ?? s])],
        v => { state.source = v; rerender() }) : null,
      pick('sparkle', 'Ordenar', state.sort, SORTS, v => { state.sort = v; localStorage.setItem('libros.comics.sort', v); rerender() })),
    list.length
      ? h('div', { class: 'grid' }, ...list.map(seriesTile))
      : h('div', { class: 'empty' }, h('h2', {}, 'Nada por aquí'), h('p', {}, {
        nuevos: 'Estás al día con todas tus series.', aldia: 'Ninguna serie al día todavía.', sinempezar: 'Has empezado todas tus series.',
        favoritas: 'Marca series con el corazón para tenerlas aquí.' }[state.tab] ?? 'Prueba con otra búsqueda.')))
}

// --- Ficha de la serie ---

async function showDetail(view, id) {
  const cached = state.list?.find(s => s.id === id)
  if (cached) view.replaceChildren(renderDetail(view, cached, null))
  else view.replaceChildren(h('div', { class: 'page' }, backLink(), h('div', { class: 'empty' }, 'Cargando…')))
  try {
    const data = await api(`api/series/${id}`)
    view.replaceChildren(renderDetail(view, data.series, data.chapters))
  } catch (e) {
    if (!cached) view.replaceChildren(h('div', { class: 'page' }, backLink(), h('div', { class: 'empty' }, h('h2', {}, 'No se pudo cargar'), h('p', {}, e.message))))
    else toast(e instanceof Offline ? 'Sin conexión' : e.message)
  }
}

const backLink = () => h('a', { class: 'back', href: '#/comics', onclick: e => { if (history.length > 1) { e.preventDefault(); history.back() } } }, icon('back'), 'Cómics')

function changed(s) {
  if (state.list) {
    const i = state.list.findIndex(x => x.id === s.id)
    if (i >= 0) state.list[i] = { ...state.list[i], ...s }
  }
  window.dispatchEvent(new CustomEvent('libros:series'))
}

function renderDetail(view, s, chapters) {
  document.title = `${s.title} · Cómics`
  const source = SOURCES[s.source] ?? s.source
  const read = n => s.last_read != null && n <= s.last_read
  const rerender = () => view.replaceChildren(renderDetail(view, s, chapters))
  const put = async (path, body, msg) => {
    try {
      const updated = await api(`api/series/${s.id}/${path}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      Object.assign(s, updated)
      changed(s)
      if (msg) toast(msg)
      rerender()
    } catch (e) { toast(e.message) }
  }
  const setRead = number => put('leido', { number })
  // Al abrir un capítulo se apunta como leído (si va después de lo leído).
  const opened = ch => { if (s.followed && !read(ch.number)) setRead(ch.number) }
  const ordered = chapters ? [...chapters].sort((a, b) => a.number - b.number) : []
  const previous = ch => { const i = ordered.findIndex(c => c.number === ch.number); return i > 0 ? ordered[i - 1].number : null }
  const follow = async () => {
    try {
      const res = await api('api/series', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source: s.source, id: s.source_id }) })
      Object.assign(s, res.series)
      chapters = res.chapters
      state.list = null
      changed(s)
      toast('Siguiendo la serie')
      rerender()
    } catch (e) { toast(e.message) }
  }

  const cover = seriesCover(s.title, s.has_cover ? `api/series/${s.id}/portada` : null, s.followed ? statusPill(s) : null)
  const total = s.latest_number
  const progress = !s.followed ? null
    : s.last_read == null
      ? h('form', { class: 'progress-line', onsubmit: e => {
        e.preventDefault()
        const v = parseFloat(new FormData(e.target).get('n'))
        if (Number.isFinite(v) && v >= 0) setRead(v)
      } }, h('span', { class: 'meta' }, '¿Ya ibas por algún capítulo? Apúntalo y te marcamos los anteriores como leídos.'),
        h('div', { class: 'actions' },
          h('input', { type: 'number', name: 'n', step: 'any', min: 0, placeholder: 'Nº de capítulo', 'aria-label': 'Último capítulo leído', style: 'max-width: 11rem' }),
          h('button', { class: 'btn small' }, 'Guardar')))
      : h('div', { class: 'progress-line' },
        h('div', { class: 'row' },
          h('span', {}, s.unread ? `Vas por el ${num(s.last_read)} de ${num(total)}` : `Al día: capítulo ${num(s.last_read)}`),
          s.unread ? h('span', { class: 'status new' }, plural(s.unread, 'nuevo', 'nuevos')) : h('span', { class: 'status ok' }, icon('check'), 'Al día')),
        h('div', { class: 'bar' }, h('i', { style: `width:${total ? Math.min(100, s.last_read / total * 100) : 0}%` })))

  const hero = h('div', { class: 'detail-hero' },
    cover,
    h('div', {},
      h('div', { class: 'eyebrow' }, [source, s.kind, s.status].filter(Boolean).join(' · ')),
      h('h1', {}, s.title),
      s.authors.length ? h('p', { class: 'by' }, s.authors.join(', ')) : null,
      progress,
      h('div', { class: 'detail-actions' },
        s.followed && s.next
          ? h('a', { class: 'btn primary big', href: s.next.url, target: '_blank', rel: 'noopener noreferrer', onclick: () => opened(s.next) },
            icon('external'), s.last_read == null ? `Empezar: capítulo ${num(s.next.number)}` : `Leer capítulo ${num(s.next.number)}`)
          : null,
        !s.followed ? h('button', { class: 'btn primary big', onclick: follow }, icon('plus'), 'Seguir la serie') : null,
        s.followed ? h('button', { class: 'icon-btn fav', 'aria-pressed': String(s.favorite), 'aria-label': s.favorite ? 'Quitar de favoritas' : 'Añadir a favoritas',
          title: s.favorite ? 'Quitar de favoritas' : 'Añadir a favoritas', onclick: () => put('favorito', { favorite: !s.favorite }, s.favorite ? 'Quitada de favoritas' : 'Añadida a favoritas') }, icon('heart')) : null,
        s.followed && s.unread ? h('button', { class: 'btn', onclick: () => setRead(s.latest_number) }, icon('check'), 'Marcar todo como leído') : null,
        menu([
          ['external', `Abrir en ${source}`, () => window.open(s.url, '_blank', 'noopener,noreferrer')],
          s.followed && s.last_read != null ? ['back', 'Empezar de nuevo', () => setRead(null)] : null,
          s.followed ? '-' : null,
          s.followed ? ['trash', 'Dejar de seguir', async () => {
            if (!confirm(`¿Dejar de seguir «${s.title}»? Se borra lo que llevas leído.`)) return
            try {
              await api(`api/series/${s.id}`, { method: 'DELETE' })
              state.list = null
              window.dispatchEvent(new CustomEvent('libros:series'))
              toast('Ya no la sigues')
              location.hash = '#/comics'
            } catch (e) { toast(e.message) }
          }, 'danger'] : null,
        ]))))
  const img = cover.querySelector('img')
  if (img) ambient(img, hero)

  const rows = (chapters ?? []).map(ch => {
    const isRead = read(ch.number)
    const early = ch.early_until && new Date(ch.early_until) > new Date()
    return h('li', { class: isRead ? 'read' : '' },
      h('a', { href: ch.url, target: '_blank', rel: 'noopener noreferrer', onclick: () => opened(ch) },
        h('span', { class: 'n' }, `Cap. ${num(ch.number)}`,
          s.followed && s.last_read != null && !isRead ? h('span', { class: 'new' }, 'Nuevo') : null,
          early ? h('span', { class: 'early', title: `Acceso anticipado de pago hasta ${new Date(ch.early_until).toLocaleString('es')}` }, '· anticipado') : null),
        h('span', { class: 'ct' }, ch.title || ''),
        h('span', { class: 'when' }, ch.published_at ? ago(ch.published_at) : '')),
      s.followed ? h('button', { class: 'icon-btn', 'aria-label': isRead ? 'Marcar como no leído' : 'Marcar leído hasta aquí',
        title: isRead ? 'Marcar como no leído' : 'Marcar leído hasta aquí', onclick: () => setRead(isRead ? previous(ch) : ch.number) }, icon('check')) : null)
  })

  return h('div', { class: 'page' },
    backLink(),
    hero,
    h('div', { class: 'detail-body comic' },
      h('div', {},
        h('div', { class: 'section-head' }, h('h2', {}, chapters ? plural(chapters.length, 'capítulo', 'capítulos') : 'Capítulos')),
        chapters ? h('ul', { class: 'chapters' }, ...rows) : h('p', { class: 'meta' }, 'Cargando capítulos…'),
        h('p', { class: 'meta small' }, `Los capítulos se leen en la web de ${source}; se comprueba si hay nuevos cada 30 minutos.`)),
      h('aside', {},
        s.description ? h('div', { class: 'card' }, h('h3', {}, 'Sinopsis'), h('div', { class: 'prose' }, s.description)) : null,
        h('div', { class: 'card' }, h('h3', {}, 'Detalles'), h('dl', { class: 'kv' }, ...[
          ['Fuente', source], ['Tipo', s.kind], ['Estado', s.status], ['Géneros', s.genres.join(', ')],
          ['Último capítulo', s.latest_at ? ago(s.latest_at) : ''], ['Comprobada', s.checked_at ? ago(s.checked_at) : ''],
        ].filter(([, v]) => v).flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)]))))))
}
