-- Coche: vehículos, repostajes, mantenimiento y gastos. Cada persona ve solo
-- lo suyo. Lo que dice algo de ella (nombres, matrícula, km, litros,
-- importes, gasolineras, notas, su municipio) va cifrado (platform/seal). En
-- claro solo el dueño, los ids, las fechas y los enlaces entre tablas.
--
-- Conservación: vehículos, repostajes, tareas y gastos hasta que la persona
-- los borra (borrar un vehículo se lleva todo lo suyo); los avisos, 7 días;
-- los precios de un municipio, mientras alguien lo tenga elegido.

-- Cada vehículo.
CREATE TABLE vehicles (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL                 -- nombre, marca y modelo, matrícula, combustible, km iniciales, nota
);
CREATE INDEX vehicles_owner ON vehicles (owner);

-- Repostajes (o cargas, en un eléctrico).
CREATE TABLE refuels (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    vehicle_id bigint NOT NULL REFERENCES vehicles ON DELETE CASCADE,
    day        date NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL                 -- km, litros, precio, total, lleno, gasolinera, nota
);
CREATE INDEX refuels_owner ON refuels (owner, vehicle_id, day);

-- Tareas de mantenimiento: cada cuántos km o meses, la última vez y la
-- próxima, y para qué vencimiento se avisó ya.
CREATE TABLE maintenance (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    vehicle_id bigint NOT NULL REFERENCES vehicles ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL
);
CREATE INDEX maintenance_owner ON maintenance (owner, vehicle_id);

-- Gastos: lo hecho de una tarea (con su coste) y los demás (seguro,
-- parking, peajes, multas…).
CREATE TABLE expenses (
    id             bigserial PRIMARY KEY,
    owner          text NOT NULL,
    vehicle_id     bigint NOT NULL REFERENCES vehicles ON DELETE CASCADE,
    maintenance_id bigint REFERENCES maintenance ON DELETE SET NULL,
    day            date NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    sealed         bytea NOT NULL             -- tipo, importe, km, nota
);
CREATE INDEX expenses_owner ON expenses (owner, vehicle_id, day);

-- Preferencias de cada persona: municipio y combustible de las gasolineras,
-- y si quiere la más barata en el resumen de la mañana.
CREATE TABLE settings (
    owner      text PRIMARY KEY,
    updated_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL
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

-- Municipios del Ministerio (todos, son públicos y no dicen nada de nadie):
-- en claro, para elegir uno.
CREATE TABLE municipios (
    id         integer PRIMARY KEY,           -- IDMunicipio del Ministerio
    nombre     text NOT NULL,
    provincia  text NOT NULL,
    fetched_at timestamptz NOT NULL
);

-- Precios de las gasolineras de un municipio. Son públicos, pero el
-- municipio delata dónde vive alguien: va como huella y el contenido,
-- cifrado.
CREATE TABLE prices (
    key_token  bigint PRIMARY KEY,            -- huella del municipio
    fetched_at timestamptz NOT NULL,
    sealed     bytea NOT NULL
);
