-- Phase 3: membership request fields + consumer fee bills (PHP billing_portal parity).

ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS request_status VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS request_box TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS fee_bills (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  user_id         BIGINT NOT NULL DEFAULT 0,
  contact_phone   VARCHAR(32) NOT NULL DEFAULT '',
  bill_type       VARCHAR(32) NOT NULL DEFAULT 'student'
                  CHECK (bill_type IN ('member','student')),
  enrollment_id   BIGINT NOT NULL DEFAULT 0,
  bill_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  paid_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  billing_month   VARCHAR(16) NOT NULL DEFAULT '',
  due_date        DATE NULL,
  payment_status  VARCHAR(32) NOT NULL DEFAULT 'pending',
  plan_name       VARCHAR(128) NOT NULL DEFAULT '',
  display_name    VARCHAR(191) NOT NULL DEFAULT '',
  transaction_id  VARCHAR(128) NOT NULL DEFAULT '',
  payment_method  VARCHAR(32) NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_fee_bills_facility_status
  ON fee_bills(facility_id, payment_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_fee_bills_phone
  ON fee_bills(contact_phone);
CREATE INDEX IF NOT EXISTS idx_fee_bills_user
  ON fee_bills(user_id);
