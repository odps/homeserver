-- Papeles: documentos personales y sus vencimientos. Es lo más delicado del
-- servidor (números de documento, escaneos del DNI o del pasaporte), así que
-- todo va cifrado con platform/seal: en claro solo el dueño, los ids y las
-- fechas de creación y cambio. Se conserva mientras la persona lo quiera;
-- cada documento y cada archivo se borra desde la web o con el asistente.

-- Un documento: tipo, título, titular, número, emisor, fechas, renovación,
-- avisos, importe y notas, todo dentro de sealed.
CREATE TABLE docs (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    sealed     bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX docs_owner ON docs (owner);

-- Archivos de un documento (escaneos, fotos, PDF). El contenido va cifrado en
-- DATA_DIR/archivos/<id> (y la miniatura en <id>.mini); aquí, cifrados, el
-- nombre original, el tipo, el tamaño y si tiene miniatura.
CREATE TABLE files (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    doc_id     bigint NOT NULL REFERENCES docs (id) ON DELETE CASCADE,
    sealed     bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX files_owner_doc ON files (owner, doc_id);
