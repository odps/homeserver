// Instalación como app. Chrome, Edge y Android avisan con «beforeinstallprompt»
// y permiten abrir su diálogo desde un botón. Safari (iPhone/iPad) no: allí
// se explica cómo añadirla a la pantalla de inicio. El aviso hay que
// capturarlo pronto, por eso este módulo se carga antes que el resto.
import { $, h, icon } from './util.js'

let deferred = null
const listeners = new Set()

window.addEventListener('beforeinstallprompt', e => {
  e.preventDefault()
  deferred = e
  listeners.forEach(fn => fn())
})
window.addEventListener('appinstalled', () => {
  deferred = null
  listeners.forEach(fn => fn())
})

export const installed = () =>
  matchMedia('(display-mode: standalone)').matches || navigator.standalone === true

const isIOS = () => /iPhone|iPad|iPod/.test(navigator.userAgent) ||
  (/Macintosh/.test(navigator.userAgent) && navigator.maxTouchPoints > 1)

// canInstall: hay forma de instalarla en este navegador (diálogo o instrucciones).
export const canInstall = () => !installed() && (deferred !== null || isIOS())

export function onInstallChange(fn) { listeners.add(fn) }

export async function install() {
  if (deferred) {
    deferred.prompt()
    await deferred.userChoice
    deferred = null
    listeners.forEach(fn => fn())
    return
  }
  // Safari: instrucciones.
  const dlg = $('#dialog')
  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Instalar Libros'), h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))),
    h('ol', { class: 'steps' },
      h('li', {}, 'Abre esta página en ', h('strong', {}, 'Safari'), '.'),
      h('li', {}, 'Pulsa el botón ', h('strong', {}, 'Compartir'), ' (el cuadrado con una flecha hacia arriba).'),
      h('li', {}, 'Elige ', h('strong', {}, 'Añadir a pantalla de inicio'), ' y confirma.')),
    h('p', { class: 'meta' }, 'Se abrirá como una app, a pantalla completa, y mantendrá tu sesión.')))
  dlg.showModal()
}

export function installHelp() {
  if (canInstall()) return install()
  const dlg = $('#dialog')
  dlg.replaceChildren(h('div', { class: 'sheet' },
    h('header', {}, h('h2', {}, 'Instalar Libros'), h('button', { class: 'icon-btn', 'aria-label': 'Cerrar', onclick: () => dlg.close() }, icon('close'))),
    installed()
      ? h('p', {}, 'Ya la estás usando como app.')
      : h('div', {},
        h('p', {}, 'Este navegador no ofrece instalarla desde aquí. Opciones:'),
        h('ul', { class: 'steps' },
          h('li', {}, h('strong', {}, 'Chrome o Edge'), ' (Android, Windows, Mac, Linux): menú ⋮ → «Instalar app» o «Añadir a pantalla de inicio».'),
          h('li', {}, h('strong', {}, 'iPhone o iPad'), ': ábrela en Safari → Compartir → «Añadir a pantalla de inicio».'),
          h('li', {}, h('strong', {}, 'Firefox'), ': en Android, menú → «Instalar»; en ordenador no instala apps web.')))))
  dlg.showModal()
}
