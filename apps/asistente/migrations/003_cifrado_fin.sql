ALTER TABLE messages DROP COLUMN content, ALTER COLUMN sealed SET NOT NULL;
ALTER TABLE notes DROP COLUMN text, ALTER COLUMN sealed SET NOT NULL;
CREATE INDEX notes_tokens ON notes USING gin (tokens);
