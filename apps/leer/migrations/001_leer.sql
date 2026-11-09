-- Leer después. Lo que guarda cada persona dice qué le interesa (las
-- direcciones, los títulos, los textos y las etiquetas), así que va cifrado
-- (platform/seal). En claro solo el dueño, los ids, las fechas, los
-- interruptores (leído, favorito, archivado), cómo fue la descarga y las
-- huellas para buscar y para no guardar dos veces el mismo enlace.
--
-- Conservación: los artículos, hasta que la persona los borra (desde la web o
-- con el asistente); los ajustes, mientras exista la cuenta; los avisos, 7 días.

-- Cada enlace guardado.
CREATE TABLE articles (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    url_token  bigint NOT NULL,                 -- huella de la dirección
    read       boolean NOT NULL DEFAULT false,
    favorite   boolean NOT NULL DEFAULT false,
    archived   boolean NOT NULL DEFAULT false,
    state      text NOT NULL DEFAULT 'cola',    -- cola, ok o error (la descarga)
    attempts   integer NOT NULL DEFAULT 0,      -- descargas fallidas seguidas
    created_at timestamptz NOT NULL DEFAULT now(),
    fetched_at timestamptz,
    read_at    timestamptz,
    tokens     bigint[] NOT NULL DEFAULT '{}',  -- huellas de las palabras y de las etiquetas
    sealed     bytea NOT NULL,                  -- dirección, título, sitio, autor, fecha, extracto, minutos, etiquetas, error
    body       bytea,                           -- el texto limpio, por bloques
    UNIQUE (owner, url_token)
);
CREATE INDEX articles_owner ON articles (owner, archived, created_at DESC);
CREATE INDEX articles_tokens ON articles USING gin (tokens);
CREATE INDEX articles_retry ON articles (state, fetched_at) WHERE state <> 'ok';

-- Ajustes de cada persona: tamaño de letra, línea en el resumen de la mañana
-- y aviso semanal de pendientes.
CREATE TABLE prefs (
    owner     text PRIMARY KEY,
    size      text NOT NULL DEFAULT 'm',
    summary   boolean NOT NULL DEFAULT true,
    weekly    boolean NOT NULL DEFAULT false,
    weekly_on date                              -- último aviso semanal
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
