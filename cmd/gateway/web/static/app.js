// Panel de homeserver: refresca cada sección sin recargar la página, pide
// confirmación en las acciones marcadas, copia los secretos recién creados y
// marca en el menú la sección que se está viendo.
// Sin dependencias ni scripts en línea (la CSP los prohíbe).

// Una sección con [data-soon] espera un cambio inminente: se mira cada 3 s.
for (const el of document.querySelectorAll("[data-refresh]")) {
  const every = (Number(el.dataset.interval) || 30) * 1000;
  const next = () => setTimeout(tick, el.querySelector("[data-soon]") ? 3000 : every);
  const tick = async () => {
    // No pisar lo que el usuario está escribiendo ni gastar en segundo plano.
    if (!(document.hidden || el.contains(document.activeElement) || el.querySelector("details[open]"))) {
      try {
        const r = await fetch(el.dataset.refresh, { headers: { Accept: "text/html" } });
        if (r.redirected && new URL(r.url).pathname === "/login") {
          location.href = "/login";
          return;
        }
        if (r.ok) el.innerHTML = await r.text();
      } catch {
        // Sin red: se reintenta en el siguiente ciclo.
      }
    }
    next();
  };
  next();
}

document.addEventListener("submit", (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !confirm(msg)) e.preventDefault();
});

document.addEventListener("click", async (e) => {
  const copy = e.target.closest("[data-copy]");
  if (copy) {
    const text = document.getElementById(copy.dataset.copy).textContent;
    try {
      await navigator.clipboard.writeText(text);
      copy.textContent = "Copiado";
    } catch {
      // Sin portapapeles (contexto no seguro): se selecciona para copiar a mano.
      getSelection().selectAllChildren(document.getElementById(copy.dataset.copy));
      copy.textContent = "Selecciónalo y cópialo";
    }
    return;
  }
  const dismiss = e.target.closest("[data-dismiss]");
  if (dismiss) dismiss.parentElement.remove();
});

// El aviso de «hecho» ya se ha visto: que no vuelva a salir al recargar.
if (new URLSearchParams(location.search).has("ok")) {
  history.replaceState(null, "", location.pathname + location.hash);
}

// Menú: resalta la sección visible y, en el móvil, la mantiene a la vista en
// la fila de secciones.
const nav = document.querySelector(".nav");
if (nav && "IntersectionObserver" in window) {
  const links = new Map([...nav.querySelectorAll('a[href^="#"]')].map((a) => [a.hash.slice(1), a]));
  const visible = new Set();
  const mark = (id) => {
    for (const [key, a] of links) {
      if (key === id) a.setAttribute("aria-current", "true");
      else a.removeAttribute("aria-current");
    }
    const a = links.get(id);
    // Solo con el menú en fila (móvil) y sin animación: un scrollTo «smooth»
    // aquí cortaría el desplazamiento suave de la página hacia el ancla.
    if (a && getComputedStyle(nav).flexDirection === "row" && nav.scrollWidth > nav.clientWidth) {
      const left = a.offsetLeft - nav.offsetLeft;
      if (left < nav.scrollLeft || left + a.offsetWidth > nav.scrollLeft + nav.clientWidth) {
        nav.scrollLeft = left - 16;
      }
    }
  };
  const io = new IntersectionObserver((entries) => {
    for (const en of entries) {
      if (en.isIntersecting) visible.add(en.target.id);
      else visible.delete(en.target.id);
    }
    // La primera sección (en orden de la página) que cruza la franja superior.
    const first = [...links.keys()].find((id) => visible.has(id));
    if (first) mark(first);
  }, { rootMargin: "-28% 0px -71% 0px" });
  for (const id of links.keys()) {
    const sec = document.getElementById(id);
    if (sec) io.observe(sec);
  }
  mark(location.hash.slice(1) && links.has(location.hash.slice(1)) ? location.hash.slice(1) : "resumen");
}

// El aviso flotante se va solo al rato (también se puede cerrar).
const flash = document.querySelector(".panel-page .flash");
if (flash) {
  setTimeout(() => {
    flash.classList.add("is-leaving");
    setTimeout(() => flash.remove(), 350);
  }, 7000);
}
