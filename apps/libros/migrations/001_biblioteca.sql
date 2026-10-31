CREATE TABLE books (
    id           bigserial PRIMARY KEY,
    sha256       text NOT NULL UNIQUE,
    format       text NOT NULL CHECK (format IN ('epub', 'pdf', 'mobi', 'azw3', 'fb2', 'fbz', 'cbz')),
    file_name    text NOT NULL,
    size         bigint NOT NULL,
    title        text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    authors      text[] NOT NULL DEFAULT '{}',
    genres       text[] NOT NULL DEFAULT '{}',
    series       text,
    series_index real,
    language     text,
    publisher    text,
    published    text,
    description  text,
    has_cover    boolean NOT NULL DEFAULT false,
    status       text NOT NULL DEFAULT 'pendiente' CHECK (status IN ('pendiente', 'leyendo', 'leido')),
    added_at     timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);

-- Una fila por libro y dispositivo: así se sabe dónde se leyó por última vez y
-- en qué punto va cada dispositivo.
CREATE TABLE progress (
    book_id     bigint NOT NULL REFERENCES books ON DELETE CASCADE,
    device_id   text NOT NULL CHECK (length(device_id) BETWEEN 1 AND 64),
    device_name text NOT NULL CHECK (length(device_name) BETWEEN 1 AND 80),
    location    text NOT NULL CHECK (length(location) <= 2000),
    fraction    real NOT NULL CHECK (fraction BETWEEN 0 AND 1),
    label       text CHECK (length(label) <= 300),
    updated_at  timestamptz NOT NULL,
    PRIMARY KEY (book_id, device_id)
);

CREATE INDEX progress_recent ON progress (updated_at DESC);
