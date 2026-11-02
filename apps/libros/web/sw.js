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

  // Navegación: a la red (la pasarela puede pedir login), pero sin esperarla
  // más de unos segundos si hay app guardada: con cobertura a medias la red no
  // falla, se queda colgada, y la app no abriría nunca.
  if (req.mode === 'navigate') {
    event.respondWith((async () => {
      const saved = await caches.match('./', { ignoreSearch: true })
      if (!saved) return fetch(req)
      try {
        const resp = await within(fetch(req), 4000)
        return resp.status >= 500 ? saved : resp // pasarela o túnel caídos: la guardada
      } catch {
        return saved
      }
    })())
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

// within rechaza si la promesa tarda más de ms.
function within(p, ms) {
  return Promise.race([p, new Promise((_, reject) => setTimeout(() => reject(new Error('sin respuesta')), ms))])
}

// networkFirst: lo último de la red y, si no hay o tarda, la última copia.
// Si la red contesta tarde, la copia se actualiza igualmente para la próxima.
async function networkFirst(name, req) {
  const cache = await caches.open(name)
  const net = fetch(req).then(resp => {
    if (resp.ok) cache.put(req, resp.clone())
    return resp
  })
  const hit = await cache.match(req)
  if (!hit) return net
  try {
    return await within(net, 5000)
  } catch {
    net.catch(() => {})
    return copy(hit)
  }
}

// copy marca una respuesta guardada para que la app sepa que el servidor no
// ha contestado (y lo diga).
function copy(resp) {
  const headers = new Headers(resp.headers)
  headers.set('X-Libros-Copia', '1')
  return new Response(resp.body, { status: resp.status, statusText: resp.statusText, headers })
}
