package students

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type MarkPaymentInput struct {
	EnrollmentID  int64    `json:"enrollment_id"`
	Status        string   `json:"status"` // paid | unpaid
	BillingMonth  string   `json:"billing_month"`
	PaidAmount    *float64 `json:"paid_amount"` // cash received (optional on unpaid)
	Reason        string   `json:"reason"`      // mandatory when marking paid
	PaymentMethod string   `json:"payment_method"`
}

type MarkPaymentResult struct {
	EnrollmentID  int64   `json:"enrollment_id"`
	PaymentStatus string  `json:"payment_status"` // overall: paid | unpaid
	UnpaidCount   int     `json:"unpaid_count"`
	BillingMonth  string  `json:"billing_month,omitempty"`
	BillID        int64   `json:"bill_id,omitempty"`
	BillAmount    float64 `json:"bill_amount,omitempty"`
	PaidAmount    float64 `json:"paid_amount,omitempty"`
	PaymentNote   string  `json:"payment_note,omitempty"`
	PaymentMethod string  `json:"payment_method,omitempty"`
}

type FeeBillRow struct {
	BillID        int64   `json:"bill_id"`
	EnrollmentID  int64   `json:"enrollment_id"`
	BillingMonth  string  `json:"billing_month"`
	BillAmount    float64 `json:"bill_amount"`
	PaidAmount    float64 `json:"paid_amount"`
	PaymentStatus string  `json:"payment_status"`
	PlanName      string  `json:"plan_name,omitempty"`
	PaymentNote   string  `json:"payment_note,omitempty"`
	PaymentMethod string  `json:"payment_method,omitempty"`
}

func normalizeBillingMonth(raw string) string {
	month := strings.TrimSpace(raw)
	if month == "" {
		return time.Now().In(istLoc()).Format("2006-01")
	}
	if len(month) >= 7 {
		return month[:7]
	}
	return month
}

func billIsPaid(status string, billAmount, paidAmount float64) bool {
	st := strings.ToLower(strings.TrimSpace(status))
	if st == "paid" {
		return true
	}
	return billAmount > 0 && paidAmount >= billAmount
}

func (s *Service) enrollmentOverallPayment(ctx context.Context, facilityID, enrollmentID int64, hasFee bool) (status string, unpaidCount int, err error) {
	err = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*)::int
FROM fee_bills b
WHERE b.facility_id=$1
  AND LOWER(COALESCE(b.bill_type,''))='student'
  AND b.enrollment_id=$2
  AND NOT (
    LOWER(COALESCE(b.payment_status,''))='paid'
    OR (COALESCE(b.bill_amount,0) > 0 AND COALESCE(b.paid_amount,0) >= COALESCE(b.bill_amount,0))
  )`, facilityID, enrollmentID).Scan(&unpaidCount)
	if err != nil {
		return "", 0, err
	}
	if unpaidCount > 0 {
		return "unpaid", unpaidCount, nil
	}
	var total int
	err = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*)::int
FROM fee_bills b
WHERE b.facility_id=$1
  AND LOWER(COALESCE(b.bill_type,''))='student'
  AND b.enrollment_id=$2`, facilityID, enrollmentID).Scan(&total)
	if err != nil {
		return "", 0, err
	}
	if total > 0 {
		return "paid", 0, nil
	}
	if hasFee {
		return "unpaid", 1, nil
	}
	return "n/a", 0, nil
}

// ListStudentFeeBills returns recent fee bills for one enrollment (month-wise).
func (s *Service) ListStudentFeeBills(ctx context.Context, facilityID, enrollmentID int64) ([]FeeBillRow, error) {
	if facilityID <= 0 || enrollmentID <= 0 {
		return nil, fmt.Errorf("invalid_request")
	}
	var enrFacility int64
	err := s.DB.QueryRowContext(ctx, `
SELECT facility_id FROM student_enrollments WHERE id=$1`, enrollmentID).Scan(&enrFacility)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("enrollment_not_found")
	}
	if err != nil {
		return nil, err
	}
	if enrFacility != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT id, enrollment_id, COALESCE(billing_month,''), COALESCE(bill_amount,0), COALESCE(paid_amount,0),
       COALESCE(payment_status,''), COALESCE(plan_name,''),
       COALESCE(payment_note,''), COALESCE(payment_method,'')
FROM fee_bills
WHERE facility_id=$1 AND LOWER(COALESCE(bill_type,''))='student' AND enrollment_id=$2
ORDER BY LEFT(COALESCE(billing_month,''),7) DESC, created_at DESC
LIMIT 24`, facilityID, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FeeBillRow{}
	for rows.Next() {
		var r FeeBillRow
		if err := rows.Scan(
			&r.BillID, &r.EnrollmentID, &r.BillingMonth, &r.BillAmount, &r.PaidAmount,
			&r.PaymentStatus, &r.PlanName, &r.PaymentNote, &r.PaymentMethod,
		); err != nil {
			return nil, err
		}
		r.BillingMonth = normalizeBillingMonth(r.BillingMonth)
		if billIsPaid(r.PaymentStatus, r.BillAmount, r.PaidAmount) {
			r.PaymentStatus = "paid"
		} else {
			r.PaymentStatus = "unpaid"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkStudentPayment upserts one month's fee_bill for the student.
// Same student can have many unpaid months — each month is its own bill.
// Paid cash amount + reason update that month only; bill_amount (plan fee) is unchanged.
func (s *Service) MarkStudentPayment(ctx context.Context, facilityID int64, in MarkPaymentInput) (*MarkPaymentResult, error) {
	if facilityID <= 0 || in.EnrollmentID <= 0 {
		return nil, fmt.Errorf("invalid_request")
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "pending" {
		status = "unpaid"
	}
	if status != "paid" && status != "unpaid" {
		return nil, fmt.Errorf("invalid_status")
	}

	reason := strings.TrimSpace(in.Reason)
	method := strings.ToLower(strings.TrimSpace(in.PaymentMethod))
	if status == "paid" {
		if reason == "" {
			return nil, fmt.Errorf("reason_required")
		}
		if in.PaidAmount == nil {
			return nil, fmt.Errorf("paid_amount_required")
		}
		if *in.PaidAmount < 0 {
			return nil, fmt.Errorf("invalid_paid_amount")
		}
		if method == "" {
			method = "cash"
		}
	} else {
		reason = ""
		method = ""
	}

	var enrFacility int64
	var planName, displayName, phone string
	var fee float64
	err := s.DB.QueryRowContext(ctx, `
SELECT e.facility_id, COALESCE(e.plan_name,''), COALESCE(e.actual_fee,0),
       TRIM(COALESCE(s.first_name,'')||' '||COALESCE(s.last_name,'')),
       COALESCE(NULLIF(s.whatsapp,''), NULLIF(s.contact_phone,''), '')
FROM student_enrollments e
JOIN students s ON s.id = e.student_id
WHERE e.id=$1`, in.EnrollmentID).Scan(&enrFacility, &planName, &fee, &displayName, &phone)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("enrollment_not_found")
	}
	if err != nil {
		return nil, err
	}
	if enrFacility != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}

	month := normalizeBillingMonth(in.BillingMonth)

	// Prefer exact month match; also accept YYYY-MM-DD stored values for same month.
	var billID int64
	var billAmount, paidAmount float64
	var payStatus, existingNote, existingMethod string
	err = s.DB.QueryRowContext(ctx, `
SELECT id, bill_amount, paid_amount, payment_status,
       COALESCE(payment_note,''), COALESCE(payment_method,'')
FROM fee_bills
WHERE facility_id=$1
  AND LOWER(COALESCE(bill_type,''))='student'
  AND enrollment_id=$2
  AND LEFT(COALESCE(billing_month,''),7)=$3
ORDER BY created_at DESC
LIMIT 1`, facilityID, in.EnrollmentID, month).Scan(
		&billID, &billAmount, &paidAmount, &payStatus, &existingNote, &existingMethod,
	)

	if err == sql.ErrNoRows {
		if fee <= 0 {
			fee = 0
		}
		billAmount = fee // plan fee snapshot — never overwritten by cash received
		if status == "paid" {
			paidAmount = *in.PaidAmount
			payStatus = "paid"
		} else {
			paidAmount = 0
			payStatus = "unpaid"
		}
		err = s.DB.QueryRowContext(ctx, `
INSERT INTO fee_bills (
  facility_id, user_id, contact_phone, bill_type, enrollment_id,
  bill_amount, paid_amount, billing_month, payment_status, plan_name, display_name,
  payment_method, payment_note
) VALUES ($1,0,$2,'student',$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id`,
			facilityID, phone, in.EnrollmentID, billAmount, paidAmount, month, payStatus,
			planName, displayName, method, reason,
		).Scan(&billID)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		// Keep existing bill_amount (plan fee). Only update paid_amount + note for this month.
		if status == "paid" {
			paidAmount = *in.PaidAmount
			payStatus = "paid"
		} else {
			paidAmount = 0
			payStatus = "unpaid"
			reason = ""
			method = ""
		}
		_, err = s.DB.ExecContext(ctx, `
UPDATE fee_bills
SET paid_amount=$2, payment_status=$3, billing_month=$4,
    payment_method=$5, payment_note=$6, updated_at=NOW()
WHERE id=$1`, billID, paidAmount, payStatus, month, method, reason)
		if err != nil {
			return nil, err
		}
	}

	overall, unpaidCount, err := s.enrollmentOverallPayment(ctx, facilityID, in.EnrollmentID, fee > 0)
	if err != nil {
		return nil, err
	}

	return &MarkPaymentResult{
		EnrollmentID:  in.EnrollmentID,
		PaymentStatus: overall,
		UnpaidCount:   unpaidCount,
		BillingMonth:  month,
		BillID:        billID,
		BillAmount:    billAmount,
		PaidAmount:    paidAmount,
		PaymentNote:   reason,
		PaymentMethod: method,
	}, nil
}
