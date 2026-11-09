-- Casa: el precio de la luz, el mantenimiento de la casa y las lecturas de
-- los contadores. Cada persona ve solo lo suyo. Lo que dice cómo es su casa
-- (las tareas y sus fechas de hecho, los contadores y sus lecturas, los
-- ajustes y los avisos) va cifrado (platform/seal). En claro solo el dueño,
-- los ids, las fechas que hacen falta para las consultas y los precios de la
-- luz, que son públicos e iguales para todos.
--
-- Conservación: tareas, hasta que la persona las borra; lecturas, 5 años
-- (las más viejas se borran solas); precios de la luz, 60 días; avisos,
-- 7 días.

-- Precios del PVPC por hora (Red Eléctrica), uno por día: públicos.
CREATE TABLE prices (
    day        date PRIMARY KEY,               -- día en Madrid
    fetched_at timestamptz NOT NULL,
    hours      jsonb NOT NULL                  -- [{"t": inicio de la hora, "p": €/kWh}]
);

-- Tareas de mantenimiento. La próxima fecha va en claro para encontrar lo
-- que toca sin descifrarlo todo; notified es la fecha de la que ya se avisó
-- (un aviso por vez).
CREATE TABLE tasks (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    due        date NOT NULL,
    notified   date,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL                  -- nombre, cada cuánto, última vez, historial, nota, plantilla
);
CREATE INDEX tasks_owner ON tasks (owner);
CREATE INDEX tasks_due ON tasks (due);

-- Lecturas de los contadores (luz, agua, gas).
CREATE TABLE readings (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    day        date NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL                  -- contador, valor, nota
);
CREATE INDEX readings_owner ON readings (owner, day);

-- Ajustes de cada persona. price_notified es el día cuyos precios ya se
-- han avisado.
CREATE TABLE settings (
    owner          text PRIMARY KEY,
    price_notified date,
    sealed         bytea NOT NULL              -- avisos, umbrales y unidad del gas
);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    sealed       bytea NOT NULL
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;
