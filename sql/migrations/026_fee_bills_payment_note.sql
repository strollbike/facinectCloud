-- Track cash received per billing month without changing plan fee (bill_amount).
ALTER TABLE fee_bills
  ADD COLUMN IF NOT EXISTS payment_note TEXT NOT NULL DEFAULT '';
