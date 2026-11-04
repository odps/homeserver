// Modo gimnasio: apunta cada serie sin recargar, cuenta el descanso y, sin
// conexión, guarda las series en el móvil hasta poder mandarlas (el servidor
// no repite una serie con el mismo id). Sin este fichero la página funciona
// igual: cada serie es un formulario normal.
(function () {
  "use strict";

  var raiz = document.querySelector(".gym[data-api]");
  if (!raiz || !window.fetch || !window.JSON) return;
  var API = raiz.getAttribute("data-api");
  var COLA = "entreno-cola";
  var MAX_COLA = 200;

  function $(id) { return document.getElementById(id); }

  // ---- Cola sin conexión (localStorage; en memoria si no se puede) ----

  var enMemoria = [];

  function leerCola() {
    try {
      var v = JSON.parse(localStorage.getItem(COLA) || "[]");
      return Array.isArray(v) ? v : [];
    } catch (e) {
      return enMemoria.slice();
    }
  }

  function guardarCola(c) {
    enMemoria = c.slice();
    try {
      if (c.length) localStorage.setItem(COLA, JSON.stringify(c));
      else localStorage.removeItem(COLA);
    } catch (e) { /* sin almacenamiento: queda en memoria */ }
  }

  function encolar(p) {
    var c = leerCola();
    if (!c.some(function (x) { return x.id === p.id; })) c.push(p);
    guardarCola(c.slice(-MAX_COLA));
    pintarCola();
  }

  function quitarDeCola(id) {
    guardarCola(leerCola().filter(function (x) { return x.id !== id; }));
    pintarCola();
  }

  function pintarCola() {
    var el = $("cola");
    if (!el) return;
    var n = leerCola().length;
    el.hidden = n === 0;
    el.textContent = n === 1 ? "Sin conexión: 1 serie pendiente" : "Sin conexión: " + n + " series pendientes";
  }

  function avisarError(msg) {
    var el = $("gym-error");
    if (!el) return;
    el.textContent = msg;
    el.hidden = false;
    clearTimeout(avisarError.t);
    avisarError.t = setTimeout(function () { el.hidden = true; }, 6000);
  }

  // enviar manda una serie. Resuelve con {estado: "ok" | "rechazada" | "fallo"}:
  // «rechazada» si el servidor dice que no es válida (no se reintenta), «fallo»
  // si no hay conexión, el servidor falla o la sesión de la pasarela caducó.
  function enviar(p) {
    return fetch(API, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(p)
    }).then(function (r) {
      var ct = r.headers.get("Content-Type") || "";
      if (ct.indexOf("application/json") < 0) return { estado: "fallo" };
      return r.json().then(function (j) {
        if (r.ok && j && j.ok) return { estado: "ok" };
        if (r.status === 400 || r.status === 413 || r.status === 415) {
          return { estado: "rechazada", error: (j && j.error) || "No se ha podido apuntar la serie." };
        }
        return { estado: "fallo" };
      });
    }).catch(function () { return { estado: "fallo" }; });
  }

  var vaciando = false;

  // vaciar manda las pendientes, de una en una y en orden.
  function vaciar() {
    if (vaciando) return;
    var c = leerCola();
    if (!c.length) { pintarCola(); return; }
    vaciando = true;
    var p = c[0];
    enviar(p).then(function (res) {
      vaciando = false;
      if (res.estado === "fallo") { pintarCola(); return; }
      quitarDeCola(p.id);
      var li = filaDe(p.id);
      if (res.estado === "rechazada") {
        if (li) desmarcar(li);
        avisarError(res.error);
      } else if (li) {
        marcar(li, p, "");
      }
      vaciar();
    });
  }

  // ---- Filas ----

  function filaDe(id) {
    var lis = document.querySelectorAll("li.serie[data-id]");
    for (var i = 0; i < lis.length; i++) {
      if (lis[i].getAttribute("data-id") === id) return lis[i];
    }
    return null;
  }

  function numero(v) {
    v = String(v == null ? "" : v).trim().replace(",", ".");
    if (v === "") return null;
    var n = Number(v);
    return isFinite(n) ? n : null;
  }

  function leerForm(f) {
    function valor(n) {
      var el = f.elements.namedItem(n);
      return el ? el.value : "";
    }
    var p = { key: valor("key"), id: valor("id"), t: Date.now() };
    ["reps", "seg", "m"].forEach(function (n) {
      var v = numero(valor(n));
      if (v !== null) p[n] = v;
    });
    var kg = numero(valor("kg"));
    if (kg !== null && kg > 0) p.kg = kg;
    var rir = numero(valor("rir"));
    if (rir !== null) p.rir = rir;
    return p;
  }

  function textoSerie(p) {
    var partes = [];
    if (p.seg) partes.push(p.seg + " s");
    else if (p.m) partes.push(String(p.m).replace(".", ",") + " m");
    else if (p.reps) partes.push(p.reps + " reps");
    if (p.kg) partes.push(String(p.kg).replace(".", ",") + " kg");
    if (p.rir != null) partes.push("RIR " + p.rir);
    return partes.join(" · ") || "Hecha";
  }

  function span(clase, texto) {
    var s = document.createElement("span");
    s.className = clase;
    s.textContent = texto;
    return s;
  }

  function contar(art, delta) {
    var c = art && art.querySelector(".gym-count");
    if (!c) return;
    var n = (parseInt(c.textContent, 10) || 0) + delta;
    c.textContent = String(n);
    var tag = c.parentNode, total = parseInt(tag.textContent.split("/")[1], 10);
    tag.classList.toggle("ok", n >= total);
  }

  // marcar pone la fila como hecha; estado: "" (enviada), "enviando" o "cola".
  function marcar(li, p, estado) {
    var f = li.querySelector("form");
    if (f) f.hidden = true;
    var hecha = li.querySelector(".serie-done");
    if (!hecha) {
      hecha = document.createElement("span");
      hecha.className = "serie-done";
      hecha.appendChild(span("serie-n", li.getAttribute("data-n") || ""));
      hecha.appendChild(span("serie-val", textoSerie(p)));
      hecha.appendChild(span("serie-ok", ""));
      li.appendChild(hecha);
      contar(li.closest(".gym-ex"), 1);
    }
    li.classList.add("hecha");
    li.classList.toggle("en-cola", estado === "cola");
    li.setAttribute("data-id", p.id);
    hecha.querySelector(".serie-ok").textContent =
      estado === "cola" ? "Pendiente" : estado === "enviando" ? "Hecha…" : "Hecha";
  }

  function desmarcar(li) {
    var hecha = li.querySelector(".serie-done");
    if (hecha) {
      hecha.remove();
      contar(li.closest(".gym-ex"), -1);
    }
    li.classList.remove("hecha", "en-cola");
    var f = li.querySelector("form");
    if (f) f.hidden = false;
  }

  document.addEventListener("submit", function (ev) {
    var f = ev.target;
    if (!f.classList || !f.classList.contains("serie-form")) return;
    ev.preventDefault();
    var li = f.closest("li");
    var art = f.closest(".gym-ex");
    var p = leerForm(f);
    prepararAudio();
    marcar(li, p, "enviando");
    var seg = parseInt(art && art.getAttribute("data-rest"), 10) || 90;
    descanso(seg, art ? art.getAttribute("data-name") : "");
    enviar(p).then(function (res) {
      if (res.estado === "ok") { marcar(li, p, ""); return; }
      if (res.estado === "rechazada") {
        desmarcar(li);
        parar();
        avisarError(res.error);
        return;
      }
      encolar(p);
      marcar(li, p, "cola");
    });
  });

  // Al cargar, las series que esperan en la cola ocupan su fila (el servidor
  // aún no las tiene).
  function marcarCola() {
    var arts = document.querySelectorAll(".gym-ex");
    leerCola().forEach(function (p) {
      for (var i = 0; i < arts.length; i++) {
        if (arts[i].getAttribute("data-key") !== p.key) continue;
        var li = arts[i].querySelector("li.serie:not(.hecha)");
        if (li) marcar(li, p, "cola");
        return;
      }
    });
  }

  // ---- Descanso ----

  var fin = 0, tic = null, cierre = null, nombre = "";
  var barra = $("descanso"), reloj = $("descanso-t"), aviso = $("descanso-aviso");

  function mmss(s) {
    var m = Math.floor(s / 60), r = s % 60;
    return m + ":" + (r < 10 ? "0" : "") + r;
  }

  function descanso(seg, quien) {
    if (!barra) return;
    nombre = quien || "";
    fin = Date.now() + seg * 1000;
    clearTimeout(cierre);
    barra.classList.remove("acabado");
    barra.hidden = false;
    if (aviso) aviso.textContent = "Descanso de " + mmss(seg);
    clearInterval(tic);
    tic = setInterval(paso, 250);
    paso();
  }

  function paso() {
    var quedan = Math.max(0, Math.ceil((fin - Date.now()) / 1000));
    reloj.textContent = mmss(quedan);
    if (quedan <= 0) terminar();
  }

  function terminar() {
    clearInterval(tic);
    tic = null;
    barra.classList.add("acabado");
    reloj.textContent = "¡Ya!";
    if (aviso) aviso.textContent = "Descanso terminado";
    try { if (navigator.vibrate) navigator.vibrate([250, 120, 250, 120, 500]); } catch (e) {}
    pitar();
    if ("Notification" in window && Notification.permission === "granted" && document.hidden) {
      try {
        new Notification("Descanso terminado", { body: nombre ? "Siguiente serie: " + nombre : "", tag: "entreno-descanso" });
      } catch (e) { /* Android pide service worker: basta con la vibración */ }
    }
    cierre = setTimeout(cerrar, 5000);
  }

  function parar() {
    clearInterval(tic);
    tic = null;
    cerrar();
  }

  function cerrar() {
    clearTimeout(cierre);
    if (barra) barra.hidden = true;
  }

  if (barra) {
    $("descanso-mas").addEventListener("click", function () {
      if (!tic) { descanso(15, nombre); return; }
      fin += 15000;
      paso();
    });
    $("descanso-saltar").addEventListener("click", parar);
  }

  // Pitido corto con WebAudio. El contexto se crea al tocar «Hecha» (los
  // navegadores no dejan sonar sin un gesto antes).
  var audio = null;

  function prepararAudio() {
    try {
      var AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return;
      if (!audio) audio = new AC();
      if (audio.state === "suspended") audio.resume();
    } catch (e) { audio = null; }
  }

  function pitar() {
    if (!audio) return;
    try {
      var t0 = audio.currentTime;
      [0, 0.3].forEach(function (d) {
        var o = audio.createOscillator(), g = audio.createGain();
        o.type = "sine";
        o.frequency.value = 880;
        g.gain.setValueAtTime(0.0001, t0 + d);
        g.gain.exponentialRampToValueAtTime(0.35, t0 + d + 0.02);
        g.gain.exponentialRampToValueAtTime(0.0001, t0 + d + 0.22);
        o.connect(g);
        g.connect(audio.destination);
        o.start(t0 + d);
        o.stop(t0 + d + 0.25);
      });
    } catch (e) {}
  }

  // ---- Pantalla encendida mientras se ve la página ----

  function pedirPantalla() {
    if (!("wakeLock" in navigator) || document.visibilityState !== "visible") return;
    navigator.wakeLock.request("screen").catch(function () {});
  }

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState !== "visible") return;
    pedirPantalla();
    if (tic) paso();
    vaciar();
  });

  window.addEventListener("online", vaciar);
  setInterval(vaciar, 30000);

  marcarCola();
  pintarCola();
  pedirPantalla();
  vaciar();
})();
