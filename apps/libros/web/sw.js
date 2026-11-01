// Service worker de Libros: la app y foliate-js se guardan al instalar para
// abrir sin conexión; los libros guardados se sirven desde el dispositivo; la
// biblioteca se pide a la red y, si no hay, se usa la última copia.
// El servidor sustituye __VERSION__ por la versión desplegada: así cada
// despliegue cambia este fichero y el navegador instala el nuevo.
const VERSION = '__VERSION__'
const SHELL = 'libros-shell'
const DATA = 'libros-data'
const COVERS = 'libros-covers'
const BOOKS = 'libros-books'

self.addEventListener('install', event => {
  event.waitUntil((async () => {
    const resp = await fetch('precache.json', { cache: 'no-store' })
    const { files } = await resp.json()
    const cache = await caches.open(`${SHELL}-${VERSION}`)
    // Por tandas y sin que un fallo suelto lo pare todo: con cache.addAll
    // bastaba un fichero que no llegase (pasarela ocupada, red móvil) para que
    // la versión nueva no se instalase nunca. Lo que falte se pide a la red.
    // cache: 'reload' evita copias viejas de la caché HTTP del navegador.
    const get = async f => {
      for (let i = 0; i < 2; i++) {
        try {
          const resp = await fetch(new Request(f, { cache: 'reload' }))
          if (resp.ok) return cache.put(f, resp)
        } catch {}
      }
    }
    for (let i = 0; i < files.length; i += 8) await Promise.all(files.slice(i, i + 8).map(get))
    await self.skipWaiting()
  })())
})

self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    // Borra las versiones anteriores de la app (los libros y portadas se quedan).
    for (const name of await caches.keys()) {
      if (name.startsWith(SHELL + '-') && name !== `${SHELL}-${VERSION}`) await caches.delete(name)
    }
    await self.clients.claim()
  })())
})

const scope = new URL(self.registration.scope)

self.addEventListener('fetch', event => {
  const req = event.request
  const url = new URL(req.url)
  if (req.method !== 'GET' || url.origin !== scope.origin || !url.pathname.startsWith(scope.pathname)) return
  const path = url.pathname.slice(scope.pathname.length)

  // Navegación: siempre a la red (la pasarela puede pedir login); sin red, la app guardada.
  if (req.mode === 'navigate') {
    event.respondWith(fetch(req).catch(async () => (await caches.match('./', { ignoreSearch: true })) ?? Response.error()))
    return
  }
  // El catálogo público solo funciona con red: no se guarda en el dispositivo.
  if (path.startsWith('api/catalog/')) return
  if (/^api\/books\/\d+\/file$/.test(path)) {
    event.respondWith(caches.open(BOOKS).then(c => c.match(req)).then(hit => hit ?? fetch(req)))
    return
  }
  // Portadas de las series que sigues; las de los resultados de búsqueda, no.
  if (/^api\/series\/\d+\/portada$/.test(path)) {
    event.respondWith(cacheFirst(COVERS, req))
    return
  }
  if (path === 'api/series/portada' || path === 'api/series/buscar') return
  if (/^api\/books\/\d+\/cover$/.test(path)) {
    event.respondWith(cacheFirst(COVERS, req))
    return
  }
  if (path.startsWith('api/')) {
    event.respondWith(networkFirst(DATA, req))
    return
  }
  // Ficheros de la app y de foliate-js, de la versión activa.
  event.respondWith(caches.open(`${SHELL}-${VERSION}`).then(c => c.match(req, { ignoreSearch: true })).then(hit => hit ?? fetch(req)))
})

async function cacheFirst(name, req) {
  const cache = await caches.open(name)
  const hit = await cache.match(req)
  if (hit) return hit
  const resp = await fetch(req)
  if (resp.ok) cache.put(req, resp.clone())
  return resp
}

async function networkFirst(name, req) {
  const cache = await caches.open(name)
  try {
    const resp = await fetch(req)
    if (resp.ok) cache.put(req, resp.clone())
    return resp
  } catch (e) {
    const hit = await cache.match(req)
    if (hit) return hit
    throw e
  }
}
