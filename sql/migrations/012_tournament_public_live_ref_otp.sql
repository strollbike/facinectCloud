-- Stage G/H: public live + referee access-code unlock
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS phone VARCHAR(32) NULL;
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS status VARCHAR(32) NOT NULL DEFAULT 'active';
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS pin_hash TEXT NULL;
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS pin_expires_at TIMESTAMPTZ NULL;
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS access_token VARCHAR(64) NULL;
ALTER TABLE tournament_referees ADD COLUMN IF NOT EXISTS assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_tournament_referees_ref
  ON tournament_referees(ref_id) WHERE ref_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS tournament_ref_profiles (
  ref_id            BIGSERIAL PRIMARY KEY,
  email             VARCHAR(191) NOT NULL,
  phone             VARCHAR(32) NULL,
  full_name         VARCHAR(191) NULL,
  referee_identity  VARCHAR(32) NULL,
  is_active         BOOLEAN NOT NULL DEFAULT TRUE,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (email)
);
CREATE INDEX IF NOT EXISTS idx_tournament_ref_profiles_identity
  ON tournament_ref_profiles(referee_identity)
  WHERE referee_identity IS NOT NULL AND referee_identity <> '';
