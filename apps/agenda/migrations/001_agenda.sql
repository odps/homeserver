-- Agenda personal: calendarios y tareas (iCalendar), contactos (vCard),
-- correo leído por IMAP y avisos pendientes de enviar por Telegram.
-- El texto iCalendar/vCard es la fuente de verdad (es lo que sincroniza el
-- móvil por CalDAV/CardDAV); las demás columnas se derivan de él al guardar.

CREATE TABLE users (
    username   text PRIMARY KEY,
    prefs       jsonb NOT NULL DEFAULT '{}',
    summary_day text NOT NULL DEFAULT '',  -- último día con resumen de la mañana
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Colecciones DAV: calendarios (kind 'cal') y libretas de contactos ('card').
CREATE TABLE collections (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL REFERENCES users ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('cal', 'card')),
    slug       text NOT NULL,
    name       text NOT NULL,
    color      text NOT NULL DEFAULT '',
    components text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner, kind, slug)
);

-- Eventos (VEVENT) y tareas (VTODO).
CREATE TABLE objects (
    id            bigserial PRIMARY KEY,
    collection_id bigint NOT NULL REFERENCES collections ON DELETE CASCADE,
    name          text NOT NULL,          -- fichero en DAV (<uid>.ics)
    uid           text NOT NULL,
    comp          text NOT NULL,          -- VEVENT o VTODO
    data          text NOT NULL,
    etag          text NOT NULL,
    summary       text NOT NULL DEFAULT '',
    location      text NOT NULL DEFAULT '',
    description   text NOT NULL DEFAULT '',
    starts_at     timestamptz,            -- primer comienzo; en tareas, el vencimiento
    ends_at       timestamptz,            -- fin de la última repetición; NULL si no acaba
    all_day       boolean NOT NULL DEFAULT false,
    recurring     boolean NOT NULL DEFAULT false,
    done          boolean NOT NULL DEFAULT false,
    next_alarm_at timestamptz,            -- próximo aviso por Telegram
    search        text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (collection_id, name)
);
CREATE INDEX objects_starts ON objects (collection_id, starts_at);
CREATE INDEX objects_alarm ON objects (next_alarm_at) WHERE next_alarm_at IS NOT NULL;

CREATE TABLE contacts (
    id            bigserial PRIMARY KEY,
    collection_id bigint NOT NULL REFERENCES collections ON DELETE CASCADE,
    name          text NOT NULL,          -- fichero en DAV (<uid>.vcf)
    uid           text NOT NULL,
    data          text NOT NULL,
    etag          text NOT NULL,
    fn            text NOT NULL DEFAULT '',
    birthday      text NOT NULL DEFAULT '', -- MM-DD
    search        text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (collection_id, name)
);

CREATE TABLE mail_accounts (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL REFERENCES users ON DELETE CASCADE,
    kind         text NOT NULL,           -- gmail, outlook, imap
    address      text NOT NULL,
    host         text NOT NULL,
    port         integer NOT NULL,
    username     text NOT NULL,
    secret       bytea NOT NULL,          -- contraseña de aplicación o refresh token, cifrados
    oauth_client text NOT NULL DEFAULT '',
    drafts       text NOT NULL DEFAULT '',
    uidvalidity  bigint NOT NULL DEFAULT 0,
    last_uid     bigint NOT NULL DEFAULT 0,
    synced_at    timestamptz,
    error        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner, address)
);

CREATE TABLE messages (
    id          bigserial PRIMARY KEY,
    account_id  bigint NOT NULL REFERENCES mail_accounts ON DELETE CASCADE,
    uid         bigint NOT NULL,
    message_id  text NOT NULL DEFAULT '',
    refs        text NOT NULL DEFAULT '',
    from_name   text NOT NULL DEFAULT '',
    from_addr   text NOT NULL DEFAULT '',
    to_addrs    text NOT NULL DEFAULT '',
    cc_addrs    text NOT NULL DEFAULT '',
    subject     text NOT NULL DEFAULT '',
    sent_at     timestamptz NOT NULL,
    body        text NOT NULL DEFAULT '',
    attachments text[] NOT NULL DEFAULT '{}',
    seen        boolean NOT NULL DEFAULT false,
    flagged     boolean NOT NULL DEFAULT false,
    search      text NOT NULL DEFAULT '',
    UNIQUE (account_id, uid)
);
CREATE INDEX messages_recent ON messages (account_id, sent_at DESC);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL REFERENCES users ON DELETE CASCADE,
    kind         text NOT NULL,
    text         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;
