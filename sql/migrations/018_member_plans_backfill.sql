-- Ensure member plan catalogue exists and backfill from membership plan_name.
CREATE TABLE IF NOT EXISTS member_plans (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  name            VARCHAR(128) NOT NULL,
  price           NUMERIC(12,2) NULL,
  duration_days   INT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_member_plans_facility ON member_plans(facility_id, status);

-- Unique name per facility (safe if already present with different constraint).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'member_plans_facility_id_name_key'
  ) THEN
    ALTER TABLE member_plans ADD CONSTRAINT member_plans_facility_id_name_key UNIQUE (facility_id, name);
  END IF;
EXCEPTION WHEN duplicate_table OR duplicate_object THEN
  NULL;
END $$;

INSERT INTO member_plans (facility_id, name, status)
SELECT DISTINCT m.facility_id, TRIM(m.plan_name), 'active'
FROM memberships m
WHERE m.plan_name IS NOT NULL AND TRIM(m.plan_name) <> ''
ON CONFLICT (facility_id, name) DO NOTHING;

-- Link memberships.plan_id when still null.
UPDATE memberships ms
SET plan_id = mp.id
FROM member_plans mp
WHERE ms.plan_id IS NULL
  AND mp.facility_id = ms.facility_id
  AND LOWER(TRIM(mp.name)) = LOWER(TRIM(COALESCE(ms.plan_name,'')));
