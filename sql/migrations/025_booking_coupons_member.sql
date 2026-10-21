-- Member-assigned booking coupons (PHP CreateCoupon / coupon_codes_{facility} parity).
ALTER TABLE booking_coupons
  ADD COLUMN IF NOT EXISTS user_id BIGINT NULL;
ALTER TABLE booking_coupons
  ADD COLUMN IF NOT EXISTS valid_until DATE NULL;
ALTER TABLE booking_coupons
  ADD COLUMN IF NOT EXISTS created_by BIGINT NULL;

CREATE INDEX IF NOT EXISTS idx_booking_coupons_user
  ON booking_coupons(facility_id, user_id);
