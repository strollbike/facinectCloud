package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(host, port, user, password, name string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		user, password, host, port, name,
	)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func EnsureSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS students (
  id              BIGSERIAL PRIMARY KEY,
  first_name      VARCHAR(96) NOT NULL,
  last_name       VARCHAR(96) NOT NULL DEFAULT '',
  contact_email   VARCHAR(191) NULL,
  contact_phone   VARCHAR(32) NULL,
  whatsapp        VARCHAR(32) NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_students_phone ON students(contact_phone);
CREATE INDEX IF NOT EXISTS idx_students_email ON students(contact_email);

CREATE TABLE IF NOT EXISTS student_enrollments (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  student_id      BIGINT NOT NULL REFERENCES students(id) ON DELETE CASCADE,
  sport_id        BIGINT NULL,
  plan_name       VARCHAR(128) NULL,
  start_date      DATE NULL,
  end_date        DATE NULL,
  actual_fee      NUMERIC(12,2) NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive','completed')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_student_enrollments_facility
  ON student_enrollments(facility_id, status);
CREATE INDEX IF NOT EXISTS idx_student_enrollments_student
  ON student_enrollments(student_id);

CREATE TABLE IF NOT EXISTS student_attendance (
  id                BIGSERIAL PRIMARY KEY,
  facility_id       BIGINT NOT NULL,
  enrollment_id     BIGINT NOT NULL REFERENCES student_enrollments(id) ON DELETE CASCADE,
  attendance_date   DATE NOT NULL,
  status            VARCHAR(16) NOT NULL
                    CHECK (status IN ('present','absent','leave')),
  marked_by         BIGINT NULL,
  notes             TEXT NULL,
  leave_reason      TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (enrollment_id, attendance_date)
);
ALTER TABLE student_attendance ADD COLUMN IF NOT EXISTS leave_reason TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_student_attendance_facility_date
  ON student_attendance(facility_id, attendance_date);

-- Coach/staff daily attendance gate before student attendance.
CREATE TABLE IF NOT EXISTS coach_attendance (
  id                BIGSERIAL PRIMARY KEY,
  facility_id       BIGINT NOT NULL,
  coach_user_id     BIGINT NOT NULL,
  attendance_date   DATE NOT NULL,
  status            VARCHAR(16) NOT NULL DEFAULT 'present'
                    CHECK (status IN ('present','absent')),
  sport_id          VARCHAR(64) NOT NULL DEFAULT '',
  notes             TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, coach_user_id, attendance_date)
);
CREATE INDEX IF NOT EXISTS idx_coach_attendance_facility_date
  ON coach_attendance(facility_id, attendance_date);

-- Facility attendance config: batch start (App Setup) + geofence radius (Administration).
CREATE TABLE IF NOT EXISTS facility_attendance_settings (
  facility_id                BIGINT PRIMARY KEY,
  batch_start_time           VARCHAR(8) NOT NULL DEFAULT '06:00',
  attendance_radius_meters   INT NOT NULL DEFAULT 150,
  facility_latitude          DOUBLE PRECISION NULL,
  facility_longitude         DOUBLE PRECISION NULL,
  reminder_offset_minutes    INT NOT NULL DEFAULT 52,
  updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE facility_attendance_settings
  ADD COLUMN IF NOT EXISTS reminder_offset_minutes INT NOT NULL DEFAULT 52;

CREATE TABLE IF NOT EXISTS student_coaching_plans (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  name            VARCHAR(128) NOT NULL,
  total_fee       NUMERIC(12,2) NOT NULL DEFAULT 0,
  days_per_week   INT NOT NULL DEFAULT 0,
  description     TEXT NOT NULL DEFAULT '',
  start_time      VARCHAR(8) NOT NULL DEFAULT '',
  end_time        VARCHAR(8) NOT NULL DEFAULT '',
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, name)
);
ALTER TABLE student_coaching_plans ADD COLUMN IF NOT EXISTS start_time VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE student_coaching_plans ADD COLUMN IF NOT EXISTS end_time VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE student_coaching_plans ADD COLUMN IF NOT EXISTS coaching_days VARCHAR(32) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_student_coaching_plans_facility
  ON student_coaching_plans(facility_id, status);

-- Cash received tracking per month (does not change plan fee / bill_amount).
ALTER TABLE fee_bills ADD COLUMN IF NOT EXISTS payment_note TEXT NOT NULL DEFAULT '';

-- Class cancelled for a coaching date. Slots in that batch window stay bookable.
CREATE TABLE IF NOT EXISTS coaching_leaves (
  id           BIGSERIAL PRIMARY KEY,
  facility_id  BIGINT NOT NULL,
  plan_id      BIGINT NOT NULL,
  leave_date   DATE NOT NULL,
  note         TEXT NOT NULL,
  created_by   BIGINT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, plan_id, leave_date)
);
CREATE INDEX IF NOT EXISTS idx_coaching_leaves_facility_date
  ON coaching_leaves(facility_id, leave_date);
`)
	return err
}
