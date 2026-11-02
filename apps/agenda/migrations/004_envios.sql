-- Envíos y pedidos que se detectan en el correo, sin IA (envios.go). Lo
-- personal (transportista, número, tienda, enlace, asunto) va cifrado en
-- "sealed"; en claro, solo el estado, la entrega prevista y las huellas del
-- número de seguimiento y del pedido, para juntar los correos de un mismo
-- envío. El paso en Go 004 busca envíos en el correo que ya estaba guardado.
CREATE TABLE shipments (
    id          bigserial PRIMARY KEY,
    account_id  bigint NOT NULL REFERENCES mail_accounts ON DELETE CASCADE,
    tokens      bigint[] NOT NULL DEFAULT '{}',
    status      text NOT NULL
                CHECK (status IN ('pedido', 'enviado', 'reparto', 'incidencia', 'recogida', 'entregado')),
    expected_on date,
    hidden      boolean NOT NULL DEFAULT false, -- quitado de la lista por la persona
    updated_at  timestamptz NOT NULL,           -- fecha del último correo del envío
    sealed      bytea NOT NULL
);
CREATE INDEX shipments_tokens ON shipments USING gin (tokens);
CREATE INDEX shipments_account ON shipments (account_id, updated_at DESC);
