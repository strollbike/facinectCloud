-- Staff facility open/close schedules (PHP sport_schedule parity) + helpers for consumer slots.

CREATE TABLE IF NOT EXISTS facility_sport_schedules (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  sport_id        BIGINT NOT NULL DEFAULT 0,          -- catalog sport id; 0 = all sports
  court_name      VARCHAR(128) NOT NULL DEFAULT '',   -- empty = all courts for sport
  day_type        VARCHAR(16) NOT NULL DEFAULT 'all'  -- all | weekday | weekend
                  CHECK (day_type IN ('all','weekday','weekend')),
  open_time       TIME NOT NULL DEFAULT '06:00',
  close_time      TIME NOT NULL DEFAULT '22:00',
  slot_minutes    INT NOT NULL DEFAULT 60
                  CHECK (slot_minutes BETWEEN 15 AND 240),
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_facility_sport_schedules_lookup
  ON facility_sport_schedules(facility_id, sport_id, day_type, status);

-- Backfill schedules from facility_courts open/close when present.
INSERT INTO facility_sport_schedules (facility_id, sport_id, court_name, day_type, open_time, close_time, slot_minutes)
SELECT
  fc.facility_id,
  COALESCE(fs.catalog_sport_id, 0),
  fc.name,
  'all',
  COALESCE(NULLIF(fc.open_time, '')::time, '06:00'::time),
  COALESCE(NULLIF(fc.close_time, '')::time, '22:00'::time),
  60
FROM facility_courts fc
LEFT JOIN facility_sports fs ON fs.id = fc.sport_id
WHERE fc.status = 'active'
  AND NOT EXISTS (
    SELECT 1 FROM facility_sport_schedules s
    WHERE s.facility_id = fc.facility_id
      AND s.court_name = fc.name
      AND s.day_type = 'all'
      AND s.sport_id = COALESCE(fs.catalog_sport_id, 0)
  );

-- Facility-wide profile hours as catch-all when no court rows exist.
INSERT INTO facility_sport_schedules (facility_id, sport_id, court_name, day_type, open_time, close_time, slot_minutes)
SELECT
  p.facility_id,
  0,
  '',
  'all',
  COALESCE(NULLIF(p.open_time, '')::time, '06:00'::time),
  COALESCE(NULLIF(p.close_time, '')::time, '22:00'::time),
  60
FROM facility_profiles p
WHERE COALESCE(NULLIF(p.open_time, ''), '') <> ''
  AND COALESCE(NULLIF(p.close_time, ''), '') <> ''
  AND NOT EXISTS (
    SELECT 1 FROM facility_sport_schedules s
    WHERE s.facility_id = p.facility_id AND s.court_name = '' AND s.day_type = 'all' AND s.sport_id = 0
  );

-- Demo Arena: explicit staff hours (not full 06–22) for Badminton Court 1/2.
INSERT INTO facility_sport_schedules (facility_id, sport_id, court_name, day_type, open_time, close_time, slot_minutes)
SELECT 1, 1, 'Court 1', 'weekday', '09:00'::time, '21:00'::time, 60
WHERE NOT EXISTS (
  SELECT 1 FROM facility_sport_schedules
  WHERE facility_id=1 AND sport_id=1 AND court_name='Court 1' AND day_type='weekday'
);
INSERT INTO facility_sport_schedules (facility_id, sport_id, court_name, day_type, open_time, close_time, slot_minutes)
SELECT 1, 1, 'Court 1', 'weekend', '08:00'::time, '22:00'::time, 60
WHERE NOT EXISTS (
  SELECT 1 FROM facility_sport_schedules
  WHERE facility_id=1 AND sport_id=1 AND court_name='Court 1' AND day_type='weekend'
);
INSERT INTO facility_sport_schedules (facility_id, sport_id, court_name, day_type, open_time, close_time, slot_minutes)
SELECT 1, 1, 'Court 2', 'all', '10:00'::time, '20:00'::time, 60
WHERE NOT EXISTS (
  SELECT 1 FROM facility_sport_schedules
  WHERE facility_id=1 AND sport_id=1 AND court_name='Court 2' AND day_type='all'
);
