-- Seguimiento de series de cómic (manga, manhwa, manhua) de webs de
-- terceros. Solo se guardan los datos de la serie y la lista de capítulos con
-- su enlace: los capítulos se leen en la web de origen.
CREATE TABLE comic_series (
    id            bigserial PRIMARY KEY,
    source        text NOT NULL CHECK (source IN ('asura', 'flame')),
    source_id     text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 150),
    title         text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    url           text NOT NULL,
    cover_url     text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT '',
    kind          text NOT NULL DEFAULT '',
    authors       text[] NOT NULL DEFAULT '{}',
    genres        text[] NOT NULL DEFAULT '{}',
    description   text NOT NULL DEFAULT '',
    latest_number real,
    latest_at     timestamptz,
    checked_at    timestamptz,
    UNIQUE (source, source_id)
);

CREATE TABLE comic_chapters (
    series_id    bigint NOT NULL REFERENCES comic_series ON DELETE CASCADE,
    number       real NOT NULL,
    title        text NOT NULL DEFAULT '',
    url          text NOT NULL,
    published_at timestamptz,
    early_until  timestamptz, -- acceso anticipado de pago hasta esa fecha
    PRIMARY KEY (series_id, number)
);

-- Qué series sigue cada persona y hasta qué capítulo ha leído.
CREATE TABLE comic_follows (
    username  text NOT NULL,
    series_id bigint NOT NULL REFERENCES comic_series ON DELETE CASCADE,
    last_read real,
    added_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (username, series_id)
);
CREATE INDEX comic_follows_series ON comic_follows (series_id);
