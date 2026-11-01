-- Noticias recogidas de los RSS (las de todas las cuentas, una sola vez) y la
-- configuración y los digest de cada cuenta.

-- Una noticia de un feed. Se guardan 8 días.
CREATE TABLE items (
    id          bigserial PRIMARY KEY,
    feed        text NOT NULL,            -- URL del feed
    source      text NOT NULL,            -- medio («El País»)
    link        text NOT NULL,
    title       text NOT NULL,
    summary     text NOT NULL DEFAULT '',
    published   timestamptz NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (feed, link)
);
CREATE INDEX items_published ON items (published DESC);

-- Estado de cada feed (para no insistir con los que fallan y para enseñarlo).
CREATE TABLE feeds (
    url         text PRIMARY KEY,
    title       text NOT NULL DEFAULT '',
    checked_at  timestamptz,
    ok_at       timestamptz,
    error       text NOT NULL DEFAULT ''
);

-- Preferencias de cada cuenta.
CREATE TABLE prefs (
    username     text PRIMARY KEY,
    digest_at    text NOT NULL DEFAULT '07:00',   -- hora (Madrid) a la que se prepara el del día
    ai_summary   boolean NOT NULL DEFAULT true,
    top_count    integer NOT NULL DEFAULT 10,     -- noticias en «Lo más importante»
    topic_count  integer NOT NULL DEFAULT 5,      -- por cada interés
    disabled     text[] NOT NULL DEFAULT '{}',    -- medios generales desactivados (claves)
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Intereses: una sección del digest. Se alimentan de la búsqueda de Google
-- News (query) y de los feeds propios asignados a la sección.
CREATE TABLE topics (
    id          bigserial PRIMARY KEY,
    username    text NOT NULL,
    name        text NOT NULL,
    query       text NOT NULL DEFAULT '',          -- vacío: solo sus feeds
    position    integer NOT NULL DEFAULT 0,
    UNIQUE (username, name)
);

-- Feeds propios de una cuenta, cada uno en una sección (un interés).
CREATE TABLE own_feeds (
    id          bigserial PRIMARY KEY,
    username    text NOT NULL,
    topic_id    bigint NOT NULL REFERENCES topics (id) ON DELETE CASCADE,
    url         text NOT NULL,
    UNIQUE (username, url)
);

-- El digest de cada cuenta y día (JSON con sus secciones).
CREATE TABLE digests (
    username    text NOT NULL,
    day         date NOT NULL,
    content     jsonb NOT NULL,
    summary     text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (username, day)
);
