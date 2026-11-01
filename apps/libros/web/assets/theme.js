// Aplica el tema guardado antes de pintar, para que no parpadee (claro u
// oscuro forzado; sin valor, el del sistema).
try {
  const t = localStorage.getItem('libros.theme')
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t
} catch {}
