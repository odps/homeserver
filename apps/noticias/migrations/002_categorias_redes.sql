-- Categorías predefinidas y fuentes de redes activadas, subforos de Reddit y
-- la sección «En el mundo».
ALTER TABLE prefs ADD COLUMN presets text[] NOT NULL DEFAULT '{}';
ALTER TABLE prefs ADD COLUMN reddit text[] NOT NULL DEFAULT '{}';
ALTER TABLE prefs ADD COLUMN world boolean NOT NULL DEFAULT true;
ALTER TABLE prefs ADD COLUMN world_count integer NOT NULL DEFAULT 8;
