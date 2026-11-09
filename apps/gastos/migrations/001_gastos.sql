-- Gastos y presupuesto. Cada persona ve solo lo suyo. Lo que dice en qué
-- gasta o cuánto (importes, conceptos, nombres de categorías y de
-- suscripciones, reglas, presupuestos, cuentas y notas) va cifrado
-- (platform/seal). En claro solo el dueño, los ids, las fechas de los
-- movimientos (para filtrar por mes), la categoría de cada uno (su id, que no
-- dice nada) y una huella del nombre de cada categoría para no repetirlas.
--
-- Conservación: categorías, reglas y suscripciones, hasta que la persona las
-- borra; los movimientos, 5 años (luego se borran solos); los avisos, 7 días.

-- Cada cuenta que ha usado la app: al entrar la primera vez se le crean las
-- categorías y las reglas de serie.
CREATE TABLE owners (
    owner      text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Categorías (las de serie y las suyas), con su presupuesto mensual.
CREATE TABLE categories (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL REFERENCES owners ON DELETE CASCADE,
    name_token bigint NOT NULL,               -- huella del nombre, para no repetirlo
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL,                -- nombre, clave de serie, presupuesto, mes ya avisado
    UNIQUE (owner, name_token)
);

-- Reglas para clasificar sin IA: «si el concepto contiene X, categoría Y».
CREATE TABLE rules (
    id          bigserial PRIMARY KEY,
    owner       text NOT NULL REFERENCES owners ON DELETE CASCADE,
    category_id bigint NOT NULL REFERENCES categories ON DELETE CASCADE,
    sealed      bytea NOT NULL                -- el texto que busca
);
CREATE INDEX rules_owner ON rules (owner);

-- Movimientos: gastos e ingresos. La fecha va en claro para filtrar por mes.
CREATE TABLE movements (
    id          bigserial PRIMARY KEY,
    owner       text NOT NULL REFERENCES owners ON DELETE CASCADE,
    day         date NOT NULL,
    category_id bigint REFERENCES categories ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    sealed      bytea NOT NULL                -- tipo, importe, concepto, cuenta, nota, origen
);
CREATE INDEX movements_owner ON movements (owner, day);

-- Suscripciones y cargos que se repiten.
CREATE TABLE subscriptions (
    id          bigserial PRIMARY KEY,
    owner       text NOT NULL REFERENCES owners ON DELETE CASCADE,
    category_id bigint REFERENCES categories ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    sealed      bytea NOT NULL                -- nombre, importe, periodicidad, próximo cobro, aviso, cuenta, nota
);
CREATE INDEX subscriptions_owner ON subscriptions (owner);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    sealed       bytea NOT NULL
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;
