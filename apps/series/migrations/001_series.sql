-- Series, pelis y juegos. Cada persona ve solo lo suyo. Lo que dice qué ve o
-- a qué juega (qué series sigue, qué capítulos ha visto, pelis, juegos,
-- valoraciones y notas) va cifrado (platform/seal). Los datos de TVmaze son
-- públicos, pero qué series hay guardadas dice qué se ve en casa: también van
-- cifrados, con una huella del id de TVmaze para no repetirlas. En claro solo
-- el dueño, los ids, las fechas y el tipo (peli o juego).
--
-- Conservación: lo de cada persona, hasta que lo borra; las series de
-- TVmaze y sus carteles, mientras alguien las siga (al dejar de seguirlas
-- se borran); los avisos, 7 días.

-- Series de TVmaze (compartidas: cada una se pide una sola vez aunque la
-- sigan dos personas).
CREATE TABLE shows (
    id         bigserial PRIMARY KEY,
    key_token  bigint NOT NULL UNIQUE,      -- huella del id de TVmaze
    checked_at timestamptz NOT NULL,        -- cuándo se pidió a TVmaze por última vez
    version    bigint NOT NULL DEFAULT 1,   -- sube con cada cambio (para la caché en memoria)
    sealed     bytea NOT NULL               -- nombre, estado, cadena, resumen y capítulos
);

-- Carteles, pedidos por el servidor y vueltos a codificar.
CREATE TABLE posters (
    show_id    bigint PRIMARY KEY REFERENCES shows ON DELETE CASCADE,
    src_token  bigint NOT NULL,             -- huella de su URL, para saber si ha cambiado
    fetched_at timestamptz NOT NULL,
    sealed     bytea NOT NULL               -- la imagen en JPEG
);

-- Las series que sigue cada persona.
CREATE TABLE follows (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    show_id    bigint NOT NULL REFERENCES shows ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL,              -- estado, valoración, nota, capítulos vistos y avisados
    UNIQUE (owner, show_id)
);

-- Pelis y juegos, apuntados a mano.
CREATE TABLE items (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('peli', 'juego')),
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL               -- título, año, plataforma, estado, valoración, fecha, nota
);
CREATE INDEX items_owner ON items (owner, kind);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    sealed       bytea NOT NULL
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;
