-- Stage E/F: match scheduling + entry payment gateway fields
ALTER TABLE tournament_fixtures
  ADD COLUMN IF NOT EXISTS match_time VARCHAR(16) NULL;

ALTER TABLE tournament_entries
  ADD COLUMN IF NOT EXISTS payment_mode VARCHAR(32) NULL;
ALTER TABLE tournament_entries
  ADD COLUMN IF NOT EXISTS paid_amount NUMERIC(12,2) NOT NULL DEFAULT 0;
ALTER TABLE tournament_entries
  ADD COLUMN IF NOT EXISTS transaction_id VARCHAR(128) NULL;
ALTER TABLE tournament_entries
  ADD COLUMN IF NOT EXISTS gateway_order_id VARCHAR(128) NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tournament_entries_txn
  ON tournament_entries(transaction_id) WHERE transaction_id IS NOT NULL AND transaction_id <> '';
