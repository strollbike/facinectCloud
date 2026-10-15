-- Geohash columns for proximity Nearby (precision 6 + coarse 5), matching PHP Facinect.
-- Backfill from latitude/longitude via app tool or scripts/backfill_geohash.py

ALTER TABLE app_facilities
  ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';

ALTER TABLE app_areas
  ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';

ALTER TABLE app_cities
  ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_app_facilities_geohash5 ON app_facilities (geohash5);
CREATE INDEX IF NOT EXISTS idx_app_facilities_geohash ON app_facilities (geohash);
CREATE INDEX IF NOT EXISTS idx_app_areas_geohash5 ON app_areas (geohash5);
