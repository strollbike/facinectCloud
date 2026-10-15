-- Phase 2: student leave reason + member play-today (PHP primary_members parity).

ALTER TABLE student_attendance
  ADD COLUMN IF NOT EXISTS leave_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS member_play_today (
  membership_id BIGINT NOT NULL,
  play_date     DATE NOT NULL,
  coming        BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (membership_id, play_date)
);
CREATE INDEX IF NOT EXISTS idx_member_play_today_date
  ON member_play_today(play_date);
