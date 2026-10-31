// Panel de homeserver: refresca cada sección sin recargar la página, pide
// confirmación en las acciones marcadas y copia el token recién creado.
// Sin dependencias ni scripts en línea (la CSP los prohíbe).

for (const el of document.querySelectorAll("[data-refresh]")) {
  const every = (Number(el.dataset.interval) || 30) * 1000;
  setInterval(async () => {
    // No pisar lo que el usuario está escribiendo ni gastar en segundo plano.
    if (document.hidden || el.contains(document.activeElement) || el.querySelector("details[open]")) return;
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
  }, every);
}

document.addEventListener("submit", (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !confirm(msg)) e.preventDefault();
});

document.addEventListener("click", async (e) => {
  const id = e.target.dataset?.copy;
  if (!id) return;
  await navigator.clipboard.writeText(document.getElementById(id).textContent);
  e.target.textContent = "Copiado";
});
