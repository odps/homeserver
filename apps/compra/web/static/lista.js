// La lista: tachar y destachar al momento, sin recargar. Sin cobertura (en
// el súper suele fallar) el cambio queda en una cola en el móvil
// (localStorage) y se manda al volver la conexión. Se manda el estado final
// («hecho»), así que reenviarlo no cambia nada. Sin este fichero la página
// funciona igual: cada fila es un formulario normal.
(function () {
  "use strict";

  var dock = document.querySelector(".dock[data-api]");
  if (!dock || !window.fetch || !window.JSON || !window.Promise) return;
  var API = dock.getAttribute("data-api");
  var COLA = "compra-cola";
  var MAX_COLA = 500;

  function $(id) { return document.getElementById(id); }

  // ---- Cola sin conexión (localStorage; en memoria si no se puede) ----

  var enMemoria = [];

  function leerCola() {
    try {
      var v = JSON.parse(localStorage.getItem(COLA) || "[]");
      return Array.isArray(v) ? v.filter(valida) : [];
    } catch (e) {
      return enMemoria.slice();
    }
  }

  function valida(x) {
    return x && typeof x.id === "number" && x.id > 0 && typeof x.hecho === "boolean";
  }

  function guardarCola(c) {
    enMemoria = c.slice();
    try {
      if (c.length) localStorage.setItem(COLA, JSON.stringify(c));
      else localStorage.removeItem(COLA);
    } catch (e) { /* sin almacenamiento: queda en memoria */ }
  }

  // De cada cosa basta su último estado.
  function encolar(id, hecho) {
    var c = leerCola().filter(function (x) { return x.id !== id; });
    c.push({ id: id, hecho: hecho });
    guardarCola(c.slice(-MAX_COLA));
  }

  // Solo se quita si sigue pidiendo lo mismo (si se volvió a tocar mientras
  // iba, queda el cambio nuevo).
  function quitarDeCola(p) {
    guardarCola(leerCola().filter(function (x) { return !(x.id === p.id && x.hecho === p.hecho); }));
  }

  var sinConexion = false;

  function pintarCola() {
    var c = leerCola();
    var pend = {};
    c.forEach(function (x) { pend[x.id] = true; });
    var lis = document.querySelectorAll("li.item[data-id]");
    for (var i = 0; i < lis.length; i++) {
      lis[i].classList.toggle("pendiente", sinConexion && !!pend[parseInt(lis[i].getAttribute("data-id"), 10)]);
    }
    var el = $("cola");
    if (!el) return;
    el.hidden = !sinConexion || c.length === 0;
    el.textContent = c.length === 1 ? "Sin conexión: 1 cambio por enviar" : "Sin conexión: " + c.length + " cambios por enviar";
  }

  function avisarError(msg) {
    var el = $("lista-error");
    if (!el) return;
    el.textContent = msg;
    el.hidden = false;
    clearTimeout(avisarError.t);
    avisarError.t = setTimeout(function () { el.hidden = true; }, 6000);
  }

  // enviar manda un cambio. Resuelve con {estado: "ok" | "rechazada" |
  // "fallo"}: «rechazada» si el servidor dice que no (no se reintenta),
  // «fallo» si no hay conexión, el servidor falla o la sesión caducó.
  function enviar(p) {
    return fetch(API, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: p.id, hecho: p.hecho })
    }).then(function (r) {
      var ct = r.headers.get("Content-Type") || "";
      if (ct.indexOf("application/json") < 0) return { estado: "fallo" };
      return r.json().then(function (j) {
        if (r.ok && j && j.ok) return { estado: "ok" };
        if (r.status >= 400 && r.status < 500 && r.status !== 401 && r.status !== 408 && r.status !== 429) {
          return { estado: "rechazada", error: (j && j.error) || "No se ha podido guardar." };
        }
        return { estado: "fallo" };
      });
    }).catch(function () { return { estado: "fallo" }; });
  }

  var vaciando = null;

  // vaciar manda lo pendiente, de uno en uno y en orden. Resuelve con true
  // si no queda nada.
  function vaciar() {
    if (vaciando) return vaciando;
    vaciando = new Promise(function (resolve) {
      function siguiente() {
        var c = leerCola();
        if (!c.length) {
          sinConexion = false;
          pintarCola();
          resolve(true);
          return;
        }
        var p = c[0];
        enviar(p).then(function (res) {
          if (res.estado === "fallo") {
            sinConexion = true;
            pintarCola();
            resolve(false);
            return;
          }
          quitarDeCola(p);
          if (res.estado === "rechazada") avisarError(res.error);
          siguiente();
        });
      }
      siguiente();
    }).then(function (ok) {
      vaciando = null;
      return ok;
    });
    return vaciando;
  }

  // ---- Filas ----

  function sumar(el, d) {
    if (!el) return;
    el.textContent = String(Math.max(0, (parseInt(el.textContent, 10) || 0) + d));
  }

  // aplicar pone la fila tachada o no. Se queda en su sitio (atenuada) para
  // que no se mueva bajo el dedo; al recargar, lo tachado va al final.
  function aplicar(li, hecho) {
    if (!li || li.classList.contains("hecho") === hecho) return;
    li.classList.toggle("hecho", hecho);
    var b = li.querySelector("button.check");
    if (b) b.setAttribute("aria-pressed", hecho ? "true" : "false");
    var inp = li.querySelector('input[name="hecho"]');
    if (inp) inp.value = hecho ? "0" : "1";
    var sec = li.closest(".sec");
    var d = hecho ? -1 : 1;
    sumar(sec && sec.querySelector(".sec-n"), d);
    sumar($("quedan"), d);
    sumar($("tachadas"), -d);
  }

  document.addEventListener("submit", function (ev) {
    var f = ev.target;
    if (!f || !f.classList) return;
    if (f.classList.contains("marcar")) {
      var id = parseInt((f.elements.namedItem("id") || {}).value, 10);
      var campo = f.elements.namedItem("hecho");
      if (!(id > 0) || !campo) return;
      ev.preventDefault();
      var hecho = campo.value === "1";
      aplicar(f.closest("li.item"), hecho);
      encolar(id, hecho);
      vaciar();
      return;
    }
    // Otro formulario (añadir, terminar…): antes, lo que espera en la cola,
    // para que el servidor lo tenga todo.
    if (leerCola().length) {
      ev.preventDefault();
      vaciar().then(function (ok) {
        if (ok) f.submit();
        else avisarError("Sin conexión: prueba cuando vuelva.");
      });
    }
  });

  // Intro en el cuadro de añadir lo manda (Mayús+Intro, otra línea).
  var texto = $("texto");
  if (texto && texto.form) {
    texto.addEventListener("keydown", function (ev) {
      if (ev.key !== "Enter" || ev.shiftKey || ev.isComposing) return;
      ev.preventDefault();
      if (!texto.value.trim()) return;
      if (texto.form.requestSubmit) texto.form.requestSubmit();
      else texto.form.submit();
    });
  }

  // Al cargar (o volver atrás), la lista refleja lo que espera en la cola.
  function reflejarCola() {
    leerCola().forEach(function (p) { aplicar($("i-" + p.id), p.hecho); });
    pintarCola();
  }

  window.addEventListener("pageshow", function (ev) {
    if (ev.persisted) reflejarCola();
  });
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") vaciar();
  });
  window.addEventListener("online", vaciar);
  setInterval(function () {
    if (leerCola().length) vaciar();
  }, 20000);

  reflejarCola();
  vaciar();
})();
