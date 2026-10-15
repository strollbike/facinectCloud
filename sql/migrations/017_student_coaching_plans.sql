-- Persistent coaching / batch plans (StrollX "Add student coaching plan").
CREATE TABLE IF NOT EXISTS student_coaching_plans (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  name            VARCHAR(128) NOT NULL,
  total_fee       NUMERIC(12,2) NOT NULL DEFAULT 0,
  days_per_week   INT NOT NULL DEFAULT 0,
  description     TEXT NOT NULL DEFAULT '',
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, name)
);
CREATE INDEX IF NOT EXISTS idx_student_coaching_plans_facility
  ON student_coaching_plans(facility_id, status);

-- Backfill catalogue from existing enrollment plan_name values.
INSERT INTO student_coaching_plans (facility_id, name, status)
SELECT DISTINCT e.facility_id, TRIM(e.plan_name), 'active'
FROM student_enrollments e
WHERE e.plan_name IS NOT NULL AND TRIM(e.plan_name) <> ''
ON CONFLICT (facility_id, name) DO NOTHING;
