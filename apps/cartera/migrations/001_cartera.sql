-- Cartera de inversión. Cada persona ve solo lo suyo. Lo que dice qué tiene o
-- qué sigue (símbolos, nombres, cantidades, precios, plataformas, notas,
-- umbrales y también los precios guardados de cada activo, que delatan cuál
-- es) va cifrado (platform/seal). En claro solo el dueño, los ids, las fechas,
-- los interruptores y una huella para no repetir un activo.
--
-- Conservación: activos, operaciones y alertas hasta que la persona los
-- borra (las operaciones hacen falta para la Renta, por FIFO); sus precios y
-- su histórico, mientras exista el activo; los avisos, 7 días.

-- Cada activo que tiene o sigue.
CREATE TABLE assets (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    key_token  bigint NOT NULL,               -- huella de fuente + id en la fuente
    watch      boolean NOT NULL DEFAULT false, -- en seguimiento
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL,                -- símbolo, nombre, tipo, fuente, moneda, precio manual, nota
    UNIQUE (owner, key_token)
);

-- Operaciones: compra, venta, comisión, dividendo, recompensa, traspaso. La
-- fecha va en claro para ordenarlas (FIFO); la hora, si se sabe, cifrada.
-- Sin activo: una comisión general de la plataforma.
CREATE TABLE operations (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    asset_id   bigint REFERENCES assets ON DELETE CASCADE,
    day        date NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL                 -- tipo, cantidad, precio, importe, comisión, moneda, plataformas, nota
);
CREATE INDEX operations_owner ON operations (owner, day, id);

-- La última cotización de cada activo.
CREATE TABLE quotes (
    asset_id   bigint PRIMARY KEY REFERENCES assets ON DELETE CASCADE,
    owner      text NOT NULL,
    updated_at timestamptz NOT NULL,
    sealed     bytea NOT NULL                 -- precio, cierre anterior, moneda
);

-- Cierres diarios de cada activo, en un bloque por activo.
CREATE TABLE history (
    asset_id   bigint PRIMARY KEY REFERENCES assets ON DELETE CASCADE,
    owner      text NOT NULL,
    updated_on date NOT NULL,                 -- para actualizarlo una vez al día
    sealed     bytea NOT NULL
);

-- Alertas, de un activo o de la cartera entera (sin activo).
CREATE TABLE alerts (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    asset_id   bigint REFERENCES assets ON DELETE CASCADE,
    active     boolean NOT NULL DEFAULT true,
    crossed    boolean NOT NULL DEFAULT false, -- ya saltó y la condición sigue: no se repite
    created_at timestamptz NOT NULL DEFAULT now(),
    fired_at   timestamptz,
    sealed     bytea NOT NULL                 -- tipo, umbral, días, una vez o cada vez
);
CREATE INDEX alerts_owner ON alerts (owner);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    sealed       bytea NOT NULL
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;

-- Cambios de divisas del BCE (Frankfurter), de todas las monedas que
-- publica: son datos públicos y, al guardarlas todas, no dicen qué monedas
-- usa nadie. Por eso van en claro.
CREATE TABLE fx (
    day   date PRIMARY KEY,
    rates jsonb NOT NULL                      -- moneda → unidades por 1 EUR
);
