// Perfil: tu cuenta (nombre visible, contraseña y dispositivos), un resumen
// de lo leído, las preferencias de la app, los ajustes de lectura (con vista
// previa) y lo propio de este dispositivo.
import { h, icon, api, toast, me, device, plural, Offline } from './util.js'
import { cachedLibrary, downloadedIds, pendingCount } from './local.js'
import { prefs, savePrefs } from './prefs.js'
import { readerSettings, saveReaderSettings, syncSettings } from './settings.js'
import { installed, installHelp } from './install.js'
import { LANGS } from './discover.js'
import { phase } from './comics.js'

export async function show(view) {
  document.title = 'Perfil · Libros'
  let books = cachedLibrary(), series = []
  const paint = extra => view.replaceChildren(render(view, books, series, extra))
  paint({})
  try {
    ;[books, series] = await Promise.all([api('api/books?sort=recent'), api('api/series').catch(() => []), syncSettings()])
  } catch (e) {
    if (!(e instanceof Offline)) toast(e.message)
  }
  paint({ downloaded: (await downloadedIds()).size, pending: await pendingCount() })
}

const seg = (value, options, onpick) => h('div', { class: 'seg', role: 'group' }, ...options.map(([v, label]) =>
  h('button', { type: 'button', 'aria-pressed': String(value === v), onclick: () => onpick(v) }, label)))

const setting = (label, hint, control) => h('div', { class: 'setting' }, h('span', {}, label, hint ? h('small', {}, hint) : null), control)

function render(view, books, series, { downloaded = 0, pending = 0 }) {
  const rerender = () => show(view)
  const year = new Date().getFullYear()
  const read = books.filter(b => b.status === 'leido')
  const thisYear = read.filter(b => b.finished_at && new Date(b.finished_at).getFullYear() === year).length
  const reading = books.filter(b => b.status === 'leyendo').length
  const unread = series.filter(s => phase(s) === 'nuevos').reduce((n, s) => n + s.unread, 0)

  return h('div', { class: 'page' },
    accountCard(view),
    h('div', { class: 'bento' },
      h('div', { class: 'accent' }, h('b', {}, read.length), h('span', {}, `libros leídos${thisYear ? ` · ${thisYear} en ${year}` : ''}`)),
      h('div', {}, h('b', {}, reading), h('span', {}, 'en curso')),
      h('div', {}, h('b', {}, series.length), h('span', {}, plural(series.length, 'serie seguida', 'series seguidas').replace(/^\d+ /, ''))),
      h('div', {}, h('b', {}, unread), h('span', {}, unread === 1 ? 'capítulo por leer' : 'capítulos por leer'))),
    h('div', { class: 'settings' },
      prefsCard(rerender),
      readerCard(),
      deviceCard(downloaded, pending),
      accountLinks()))
}

// --- Cuenta ---

function accountCard(view) {
  const initial = (me.name || me.user || '?').trim()[0]?.toUpperCase() ?? '?'
  const nameBox = h('div', {})
  const paintName = () => nameBox.replaceChildren(
    h('h1', {}, me.name || me.user),
    h('div', { class: 'meta' }, `@${me.user}${me.admin ? ' · administrador' : ''}`),
    h('button', { class: 'linkish', onclick: edit }, 'Cambiar nombre'))
  const edit = () => {
    const input = h('input', { type: 'text', value: me.name || '', maxlength: 60, 'aria-label': 'Nombre visible', style: 'max-width: 18rem' })
    const save = async e => {
      e.preventDefault()
      try {
        const resp = await fetch('_cuenta/nombre', { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ name: input.value }) })
        const out = await resp.json().catch(() => ({}))
        if (!resp.ok) throw new Error(out.error ?? `error ${resp.status}`)
        me.name = out.name
        try { localStorage.setItem('libros.me', JSON.stringify(me)) } catch {}
        window.dispatchEvent(new Event('libros:me'))
        toast('Nombre cambiado')
        view.querySelector('.profile-card .avatar').textContent = me.name.trim()[0].toUpperCase()
        paintName()
      } catch (err) { toast(err.message) }
    }
    nameBox.replaceChildren(h('form', { class: 'actions', onsubmit: save }, input,
      h('button', { class: 'btn primary small' }, 'Guardar'),
      h('button', { type: 'button', class: 'btn ghost small', onclick: paintName }, 'Cancelar')),
    h('div', { class: 'meta', style: 'margin-top:.4rem' }, `Para entrar se sigue usando el usuario @${me.user}.`))
    input.focus()
    input.select()
  }
  paintName()
  return h('div', { class: 'profile-card' }, h('span', { class: 'avatar', 'aria-hidden': 'true' }, initial), nameBox)
}

function accountLinks() {
  return h('div', { class: 'card' },
    h('h2', {}, 'Cuenta y seguridad'),
    h('p', { class: 'meta' }, 'Tu sesión dura mientras uses la app (se cierra tras 90 días sin uso).'),
    h('a', { class: 'linkrow', href: '_cuenta' }, icon('key'), h('span', {}, 'Contraseña y dispositivos conectados'), icon('chevron')),
    !installed() ? h('button', { class: 'linkrow', onclick: () => installHelp() }, icon('download'), h('span', {}, 'Instalar la app en este dispositivo'), icon('chevron')) : null,
    h('form', { method: 'post', action: '_logout', class: 'linkform' },
      h('button', { class: 'linkrow danger' }, icon('logout'), h('span', {}, 'Cerrar sesión'))))
}

// --- Preferencias ---

function prefsCard(rerender) {
  const p = prefs()
  const set = async patch => {
    try { await savePrefs(patch); toast('Preferencia guardada') } catch (e) { toast(e.message) }
    rerender()
  }
  return h('div', { class: 'card' },
    h('h2', {}, 'Preferencias'),
    h('p', { class: 'meta' }, 'Se guardan en tu cuenta y se aplican en todos tus dispositivos.'),
    setting('Tema', null, seg(p.theme, [['system', 'Sistema'], ['light', 'Claro'], ['dark', 'Oscuro']], v => set({ theme: v }))),
    setting('Idioma al buscar libros', 'En Descubrir, bibliotecas públicas', h('select', { onchange: e => set({ search_lang: e.target.value }) },
      ...LANGS.map(([v, l]) => h('option', { value: v, selected: p.search_lang === v }, l)))),
    setting('Fuente de cómics', 'Dónde buscar series por defecto', h('select', { onchange: e => set({ comic_source: e.target.value }) },
      ...[['todas', 'Todas'], ['asura', 'Asura Scans'], ['flame', 'Flame Comics']].map(([v, l]) => h('option', { value: v, selected: p.comic_source === v }, l)))),
    setting('Al añadir libros', 'Quién los verá', seg(p.visibility, [['privado', 'Solo yo'], ['compartido', 'Todos']], v => set({ visibility: v }))))
}

// --- Ajustes de lectura ---

const READER_THEMES = { light: ['#ffffff', '#1d1c1a'], sepia: ['#f4ecd8', '#5b4636'], dark: ['#121212', '#d7d4cd'] }
const FONTS = { original: 'var(--serif)', serif: 'Georgia, "Iowan Old Style", serif', sans: 'system-ui, sans-serif' }

function readerCard() {
  const card = h('div', { class: 'card' })
  const paint = () => {
    const s = readerSettings()
    const set = (k, v) => { saveReaderSettings({ ...s, [k]: v }); paint() }
    const [bg, fg] = READER_THEMES[s.theme] ?? READER_THEMES.light
    const pad = { narrow: '.8rem', normal: '1.2rem', wide: '1.8rem' }[s.margin]
    card.replaceChildren(
      h('h2', {}, 'Lectura'),
      h('p', { class: 'meta' }, 'Se aplican a todos tus libros en cualquier dispositivo (en PDF y cómics, solo el tema).'),
      setting('Tema del lector', null, seg(s.theme, [['light', 'Claro'], ['sepia', 'Sepia'], ['dark', 'Oscuro']], v => set('theme', v))),
      setting(`Tamaño · ${s.size} %`, null, h('div', { class: 'seg' },
        h('button', { type: 'button', 'aria-label': 'Más pequeño', onclick: () => set('size', Math.max(70, s.size - 10)) }, 'A−'),
        h('button', { type: 'button', 'aria-label': 'Más grande', onclick: () => set('size', Math.min(200, s.size + 10)) }, 'A+'))),
      setting('Letra', null, seg(s.font, [['original', 'Del libro'], ['serif', 'Serif'], ['sans', 'Sans']], v => set('font', v))),
      setting('Interlineado', null, seg(s.spacing, [[1.3, 'Compacto'], [1.5, 'Normal'], [1.8, 'Amplio']], v => set('spacing', v))),
      setting('Márgenes', null, seg(s.margin, [['narrow', 'Estrechos'], ['normal', 'Normales'], ['wide', 'Amplios']], v => set('margin', v))),
      setting('Alineación', null, seg(s.justify, [[true, 'Justificada'], [false, 'Izquierda']], v => set('justify', v))),
      setting('Modo', null, seg(s.flow, [['paginated', 'Páginas'], ['scrolled', 'Desplazamiento']], v => set('flow', v))),
      h('div', { class: 'sample', style: `background:${bg};color:${fg};font-family:${FONTS[s.font]};font-size:${s.size}%;line-height:${s.spacing};text-align:${s.justify ? 'justify' : 'left'};padding:1rem ${pad};hyphens:auto` },
        h('p', { lang: 'es' }, 'En un lugar de la Mancha, de cuyo nombre no quiero acordarme, no ha mucho tiempo que vivía un hidalgo de los de lanza en astillero, adarga antigua, rocín flaco y galgo corredor.')))
  }
  paint()
  return card
}

// --- Este dispositivo ---

function deviceCard(downloaded, pending) {
  const d = device()
  return h('div', { class: 'card' },
    h('h2', {}, 'Este dispositivo'),
    h('p', { class: 'meta' }, 'Sirve para saber dónde dejaste cada libro.'),
    setting('Nombre', 'Se ve en el progreso de cada libro', h('input', { type: 'text', value: localStorage.getItem('libros.device.name') ?? '', placeholder: d.name,
      onchange: e => { localStorage.setItem('libros.device.name', e.target.value.trim()); toast('Nombre del dispositivo guardado') } })),
    setting('Guardados sin conexión', null, h('span', { class: 'meta' }, plural(downloaded, 'libro', 'libros'))),
    pending ? setting('Pendiente de sincronizar', null, h('span', { class: 'status new' }, plural(pending, 'libro', 'libros'))) : null)
}
