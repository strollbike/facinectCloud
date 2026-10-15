package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// checkoutQuote mirrors PHP buildQuote for app booking_checkout.
type checkoutQuote struct {
	TotalBill      float64
	AdvancePay     float64
	Discount       float64
	Payable        float64
	BalanceDue     float64
	PaymentMode    string
	ModeAllowed    bool
	CouponCode     string
	CouponApplied  bool
	IsRegOffer     bool
	IsUserRefund   bool
}

func normalizePayMode(raw, method string) string {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m != "" {
		return m
	}
	ml := strings.ToLower(strings.TrimSpace(method))
	switch ml {
	case "spot":
		return "spot"
	case "coupon":
		return "free"
	default:
		return "full"
	}
}

func (s *Service) resolveCouponDiscount(ctx context.Context, facilityID, userID int64, code string) (discount float64, isReg, isUserRefund bool, err error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, false, false, nil
	}
	upper := strings.ToUpper(code)
	if strings.HasPrefix(upper, "REG-") {
		isReg = true
	}

	// Facility promo / flat coupons.
	var value float64
	var ctype string
	qerr := s.DB.QueryRowContext(ctx, `
SELECT value, COALESCE(ctype,'flat') FROM app_coupons
WHERE facility_id=$1 AND UPPER(code)=$2 AND status='active'`, facilityID, upper).Scan(&value, &ctype)
	if qerr == nil {
		if strings.EqualFold(ctype, "percent") || strings.EqualFold(ctype, "percentage") {
			return value, isReg, false, nil // percent applied later against bill
		}
		return value, isReg, false, nil
	}

	// User refund / assigned coupons (REF-*).
	var amt float64
	var exp *time.Time
	uerr := s.DB.QueryRowContext(ctx, `
SELECT amount, expiry_date FROM app_user_coupons
WHERE facility_id=$1 AND UPPER(coupon_code)=$2
  AND ($3=0 OR user_id=$3)
  AND (expiry_date IS NULL OR expiry_date >= CURRENT_DATE)
ORDER BY id DESC LIMIT 1`, facilityID, upper, userID).Scan(&amt, &exp)
	if uerr == nil && amt > 0 {
		return amt, isReg, true, nil
	}

	return 0, isReg, false, fmt.Errorf("Invalid coupon")
}

func applyDiscount(total, discountRaw float64, ctypeHint string) float64 {
	if discountRaw <= 0 || total <= 0 {
		return 0
	}
	if strings.EqualFold(ctypeHint, "percent") || strings.EqualFold(ctypeHint, "percentage") {
		d := total * (discountRaw / 100.0)
		if d > total {
			return total
		}
		return d
	}
	if discountRaw > total {
		return total
	}
	return discountRaw
}

func (s *Service) couponCType(ctx context.Context, facilityID int64, code string) string {
	var ctype string
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(ctype,'flat') FROM app_coupons
WHERE facility_id=$1 AND UPPER(code)=$2 AND status='active'`, facilityID, strings.ToUpper(strings.TrimSpace(code))).Scan(&ctype)
	if ctype == "" {
		return "flat"
	}
	return ctype
}

func (s *Service) buildCheckoutQuote(
	ctx context.Context,
	facilityID, sportID, userID int64,
	slots []any,
	paymentMode, couponCode string,
) (checkoutQuote, error) {
	q := checkoutQuote{PaymentMode: normalizePayMode(paymentMode, ""), CouponCode: strings.TrimSpace(couponCode)}
	total, advancePay, _ := s.computeCheckoutAmounts(ctx, facilityID, sportID, slots)
	q.TotalBill = total
	q.AdvancePay = advancePay

	var discRaw float64
	var err error
	if q.CouponCode != "" {
		discRaw, q.IsRegOffer, q.IsUserRefund, err = s.resolveCouponDiscount(ctx, facilityID, userID, q.CouponCode)
		if err != nil {
			return q, err
		}
		ctype := s.couponCType(ctx, facilityID, q.CouponCode)
		q.Discount = applyDiscount(total, discRaw, ctype)
		// Percent stored as value in app_coupons: resolveCouponDiscount returns raw percent;
		// re-apply if ctype is percent and discRaw looks like percent (already handled).
		if ctype == "percent" || ctype == "percentage" {
			q.Discount = applyDiscount(total, discRaw, ctype)
		}
		q.CouponApplied = q.Discount > 0 || q.CouponCode != ""
	}

	netBill := total - q.Discount
	if netBill < 0 {
		netBill = 0
	}

	// Coupon applied → PHP forces full (or free if 100% off); no advance/spot.
	if q.CouponApplied {
		if netBill <= 0.009 {
			q.PaymentMode = "free"
			q.Payable = 0
			q.BalanceDue = 0
			q.ModeAllowed = true
			return q, nil
		}
		if q.PaymentMode == "advance" || q.PaymentMode == "spot" {
			q.PaymentMode = "full"
		}
	}

	switch q.PaymentMode {
	case "spot":
		q.Payable = 0
		q.BalanceDue = netBill
		q.ModeAllowed = netBill > 0
	case "free":
		q.Payable = 0
		q.BalanceDue = 0
		q.ModeAllowed = true
	case "advance":
		adv := advancePay
		if adv > netBill {
			adv = netBill
		}
		if adv <= 0 || adv >= netBill {
			q.ModeAllowed = false
			return q, fmt.Errorf("Advance payment is not available for these slots.")
		}
		q.Payable = adv
		q.BalanceDue = netBill - adv
		q.ModeAllowed = true
	default: // full
		q.PaymentMode = "full"
		q.Payable = netBill
		q.BalanceDue = 0
		q.ModeAllowed = netBill > 0 || q.CouponApplied
		if q.Payable <= 0 && q.CouponApplied {
			q.PaymentMode = "free"
			q.ModeAllowed = true
		}
	}
	return q, nil
}

func verifyRazorpaySignature(orderID, paymentID, signature, secret string) bool {
	if orderID == "" || paymentID == "" || signature == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(orderID + "|" + paymentID))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature)))) ||
		hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature)))
}

func (s *Service) consumeCheckoutCoupon(ctx context.Context, facilityID, userID int64, q checkoutQuote, bookingID int64) {
	if q.CouponCode == "" {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(q.CouponCode))
	if q.IsRegOffer {
		// Soft mark — registrations table may not exist in app DB.
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_coupons SET status='used' WHERE facility_id=$1 AND UPPER(code)=$2 AND status='active'`,
			facilityID, code)
		return
	}
	if q.IsUserRefund {
		_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_user_coupons
WHERE facility_id=$1 AND UPPER(coupon_code)=$2 AND ($3=0 OR user_id=$3)`,
			facilityID, code, userID)
		return
	}
	// Promo: leave active (multi-use) unless single-use seed; bump nothing for now.
	_ = bookingID
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:n*2]
	}
	return hex.EncodeToString(b)
}

// mintCancelRefundCoupon mirrors PHP cancel_booking_dynamic REF-* issuance.
func (s *Service) mintCancelRefundCoupon(
	ctx context.Context,
	userID, facilityID, bookingID int64,
	paidAmount float64,
	notes, priorCoupon string,
) (couponCode string, amount float64, expiry string) {
	if paidAmount <= 0 {
		return "", 0, ""
	}
	// PHP: no refund coupon if booking already used a coupon / reg offer.
	if strings.TrimSpace(priorCoupon) != "" {
		return "", 0, ""
	}
	if strings.Contains(strings.ToLower(notes), "reg offer") ||
		strings.Contains(strings.ToLower(notes), "coupon") {
		return "", 0, ""
	}
	code := fmt.Sprintf("REF-%d-%s", bookingID, strings.ToUpper(randomHex(2)))
	exp := time.Now().In(bookingTZ()).AddDate(0, 0, 30)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_user_coupons (user_id, facility_id, coupon_code, amount, expiry_date)
VALUES ($1,$2,$3,$4,$5)`,
		userID, facilityID, code, paidAmount, exp.Format("2006-01-02"))
	if err != nil {
		return "", 0, ""
	}
	return code, paidAmount, exp.Format("2006-01-02")
}

func (s *Service) loadSportPaymentFlags(ctx context.Context, facilityID, sportID int64) (full, advance, spot bool, sportAdv float64) {
	full, advance, spot = true, true, true
	var af, aa, aspot int
	var adv float64
	err := s.DB.QueryRowContext(ctx, `
SELECT
  CASE WHEN allow_full_payment THEN 1 ELSE 0 END,
  CASE WHEN allow_advance_payment THEN 1 ELSE 0 END,
  CASE WHEN allow_spot_payment THEN 1 ELSE 0 END,
  COALESCE(advance_amount,0)
FROM facility_sport_payments
WHERE facility_id=$1 AND (sport_id=$2 OR sport_id=0)
ORDER BY CASE WHEN sport_id=$2 THEN 0 ELSE 1 END
LIMIT 1`, facilityID, sportID).Scan(&af, &aa, &aspot, &adv)
	if err == nil {
		full, advance, spot = af == 1, aa == 1, aspot == 1
		sportAdv = adv
	}
	return
}

func quoteToMap(q checkoutQuote) map[string]any {
	return map[string]any{
		"totalBill":     q.TotalBill,
		"advanceAmount": q.AdvancePay,
		"discount":      q.Discount,
		"payable":       q.Payable,
		"balanceDue":    q.BalanceDue,
		"paymentMode":   q.PaymentMode,
		"modeAllowed":   q.ModeAllowed,
		"couponCode":    q.CouponCode,
		"couponApplied": q.CouponApplied,
	}
}

func parseFloatAny(v any) float64 {
	f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil {
		return 0
	}
	return f
}

func couponCodeFromNotes(notes string) string {
	n := strings.TrimSpace(notes)
	if n == "" {
		return ""
	}
	lower := strings.ToLower(n)
	for _, prefix := range []string{"coupon:", "reg offer applied:"} {
		if i := strings.Index(lower, prefix); i >= 0 {
			return strings.TrimSpace(n[i+len(prefix):])
		}
	}
	return ""
}
