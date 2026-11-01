// Inicio: lo que importa al abrir la app. Lo que estabas leyendo (en grande),
// los capítulos nuevos de tus series y tus favoritos. Nada más.
import { h, icon, api, ago, pct, me, ambient, Offline } from './util.js'
import { cachedLibrary } from './local.js'
import { bookTile, bookCover } from './books.js'
import { seriesTile } from './comics.js'

function greeting() {
  const hour = new Date().getHours()
  const part = hour < 6 ? 'Buenas noches' : hour < 13 ? 'Buenos días' : hour < 21 ? 'Buenas tardes' : 'Buenas noches'
  const name = (me.name || me.user || '').split(' ')[0]
  return h('h1', { class: 'greeting' }, `${part}${name ? ',' : ''} `, name ? h('em', {}, name) : null)
}

export async function show(view) {
  document.title = 'Libros'
  let books = cachedLibrary()
  let series = []
  const paint = () => view.replaceChildren(render(books, series))
  paint()
  try {
    ;[books, series] = await Promise.all([api('api/books?sort=recent'), api('api/series').catch(() => [])])
    window.dispatchEvent(new CustomEvent('libros:series', { detail: series }))
    paint()
  } catch (e) {
    if (!(e instanceof Offline)) throw e
  }
}

function render(books, series) {
  const reading = books.filter(b => b.status === 'leyendo' || (b.last && b.status !== 'leido'))
  const hero = reading[0]
  const fresh = series.filter(s => s.last_read != null && s.unread > 0)
  const favBooks = books.filter(b => b.favorite)
  const favSeries = series.filter(s => s.favorite)
  const page = h('div', { class: 'page home' },
    greeting(),
    h('p', { class: 'meta' }, new Date().toLocaleDateString('es', { weekday: 'long', day: 'numeric', month: 'long' })))

  if (!books.length && !series.length) {
    page.append(h('div', { class: 'welcome' },
      h('a', { href: '#/libros' }, icon('book'), h('strong', {}, 'Añade tu primer libro'), h('span', { class: 'meta' }, 'Sube EPUB, PDF, MOBI o CBZ y léelos en cualquier dispositivo.')),
      h('a', { href: '#/descubrir/libros' }, icon('compass'), h('strong', {}, 'Descubre clásicos'), h('span', { class: 'meta' }, 'Miles de libros de dominio público, gratis y legales.')),
      h('a', { href: '#/descubrir/comics' }, icon('layers'), h('strong', {}, 'Sigue tus manhwa'), h('span', { class: 'meta' }, 'Te avisamos de cada capítulo nuevo.'))))
    return page
  }

  if (hero) page.append(h('div', { class: 'section' }, heroCard(hero)))
  if (reading.length > 1) {
    page.append(section('Seguir leyendo', '#/libros/leyendo', h('div', { class: 'shelf' }, ...reading.slice(1, 16).map(bookTile))))
  }
  if (fresh.length) {
    const total = fresh.reduce((n, s) => n + s.unread, 0)
    page.append(section(`Capítulos nuevos · ${total}`, '#/comics', h('div', { class: 'shelf' }, ...fresh.map(seriesTile))))
  }
  if (favBooks.length || favSeries.length) {
    page.append(section('Favoritos', null, h('div', { class: 'shelf' }, ...favBooks.map(bookTile), ...favSeries.map(seriesTile))))
  }
  const pending = books.filter(b => b.status === 'pendiente' && !b.last).slice(0, 16)
  if (pending.length && !hero) page.append(section('Por empezar', '#/libros', h('div', { class: 'shelf' }, ...pending.map(bookTile))))
  return page
}

function section(title, href, body) {
  return h('section', { class: 'section' },
    h('div', { class: 'section-head' }, h('h2', {}, title), href ? h('a', { href }, 'Ver todo') : null),
    body)
}

function heroCard(b) {
  const cover = bookCover(b)
  const card = h('a', { class: 'hero', href: `#/leer/${b.id}` },
    cover,
    h('div', {},
      h('div', { class: 'eyebrow' }, 'Continuar leyendo'),
      h('h2', {}, b.title),
      h('div', { class: 'meta' }, b.authors.join(', ')),
      b.last ? h('div', { class: 'bar' }, h('i', { style: `width:${b.last.fraction * 100}%` })) : null,
      b.last ? h('div', { class: 'meta small' }, `${pct(b.last.fraction)}${b.last.label ? ' · ' + b.last.label : ''} · ${ago(b.last.updated_at)} en ${b.last.device_name}`) : null,
      h('span', { class: 'btn primary' }, icon('play'), 'Continuar')))
  const img = cover.querySelector('img')
  if (img) ambient(img, card)
  return card
}
