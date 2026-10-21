package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) ensureFeeBills(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS fee_bills (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  user_id         BIGINT NOT NULL DEFAULT 0,
  contact_phone   VARCHAR(32) NOT NULL DEFAULT '',
  bill_type       VARCHAR(32) NOT NULL DEFAULT 'student',
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
)`)
}

func (s *Service) BillingPortal(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	switch action {
	case "my_bills":
		return s.billingMyBills(ctx, body)
	case "pay_url":
		fid, _ := parseInt64Field(body, "facilityId")
		return true, "OK", map[string]any{
			"facilityId": fid,
			"payUrl":     "",
		}
	case "payment_options":
		fid, _ := parseInt64Field(body, "facilityId")
		if fid <= 0 {
			return false, "facilityId is required.", nil
		}
		key := strings.TrimSpace(s.Cfg.RazorpayKeyID)
		return true, "OK", map[string]any{
			"facilityId":                    fid,
			"allow_member_online_payment":   1,
			"allow_student_online_payment":  1,
			"allowMemberOnline":             1,
			"allowStudentOnline":            1,
			"currency":                      "INR",
			"razorpayConfigured":            boolToInt(key != ""),
			"keyId":                         key,
		}
	case "initiate_payment":
		return s.billingInitiate(ctx, body)
	case "confirm_payment":
		return s.billingConfirm(ctx, body)
	case "send_payment_reminder", "payment_reminder":
		return s.billingSendPaymentReminder(ctx, body)
	default:
		return false, "unknown_action", nil
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Service) billingMyBills(ctx context.Context, body map[string]any) (bool, string, any) {
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	uid, _ := parseInt64Field(body, "userId")
	wa := s.resolvePortalWhatsApp(ctx, body)
	s.ensureFeeBills(ctx)

	typeFilter := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["type"])))
	statusFilter := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["status"])))
	facilityFilter, _ := parseInt64Field(body, "facilityId")

	bills := []map[string]any{}
	seen := map[string]bool{}

	appendBill := func(b map[string]any) {
		key := fmt.Sprintf("%v:%v:%v", b["type"], b["enrollmentId"], b["billingMonth"])
		if seen[key] {
			return
		}
		seen[key] = true
		bills = append(bills, b)
	}

	phoneMatch := phoneLast10SQL("b.contact_phone")
	q := fmt.Sprintf(`
SELECT b.id, b.enrollment_id, b.bill_type, b.bill_amount, b.paid_amount, b.billing_month,
       COALESCE(b.due_date::text,''), b.payment_status, b.plan_name, b.display_name, b.facility_id,
       COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), ''), COALESCE(af.logo_url,''),
       b.transaction_id, b.payment_method, b.contact_phone
FROM fee_bills b
LEFT JOIN app_facilities af ON af.id=b.facility_id
LEFT JOIN facilities f ON f.id=b.facility_id
WHERE (%s=$1 OR ($2>0 AND b.user_id=$2))`, phoneMatch)
	args := []any{last10, uid}
	argN := 3
	if facilityFilter > 0 {
		q += fmt.Sprintf(` AND b.facility_id=$%d`, argN)
		args = append(args, facilityFilter)
		argN++
	}
	if typeFilter == "member" || typeFilter == "student" {
		q += fmt.Sprintf(` AND b.bill_type=$%d`, argN)
		args = append(args, typeFilter)
		argN++
	}
	q += ` ORDER BY b.created_at DESC`

	if rows, err := s.DB.QueryContext(ctx, q, args...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, eid, fid int64
			var btype, month, due, status, plan, display, fname, logo, txn, method, phone string
			var amount, paid float64
			_ = rows.Scan(&id, &eid, &btype, &amount, &paid, &month, &due, &status, &plan, &display, &fid, &fname, &logo, &txn, &method, &phone)
			if statusFilter == "pending" && (status == "paid" || amount-paid <= 0) {
				continue
			}
			if statusFilter == "paid" && status != "paid" && amount-paid > 0 {
				continue
			}
			appendBill(s.billRow(id, eid, btype, amount, paid, month, due, status, plan, display, fid, fname, logo, txn, method, phone))
		}
	}

	// Also surface unpaid memberships / student fees as synthetic pending bills.
	if statusFilter != "paid" {
		s.appendSyntheticMemberBills(ctx, last10, facilityFilter, typeFilter, appendBill)
		s.appendSyntheticStudentBills(ctx, last10, facilityFilter, typeFilter, appendBill)
	}

	// Fallback app_fee_bills by userId
	if len(bills) == 0 && uid > 0 {
		rows, err := s.DB.QueryContext(ctx, `
SELECT b.id, b.enrollment_id, b.bill_type, b.bill_amount, b.paid_amount, b.billing_month,
       COALESCE(b.due_date::text,''), b.payment_status, b.plan_name, b.display_name, b.facility_id,
       COALESCE(f.name,''), COALESCE(f.logo_url,''), b.transaction_id
FROM app_fee_bills b
LEFT JOIN app_facilities f ON f.id=b.facility_id
WHERE b.user_id=$1 ORDER BY b.created_at DESC`, uid)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, eid, fid int64
				var btype, month, due, status, plan, display, fname, logo, txn string
				var amount, paid float64
				_ = rows.Scan(&id, &eid, &btype, &amount, &paid, &month, &due, &status, &plan, &display, &fid, &fname, &logo, &txn)
				appendBill(s.billRow(id, eid, btype, amount, paid, month, due, status, plan, display, fid, fname, logo, txn, "", wa))
			}
		}
	}

	pendingCount, paidCount := 0, 0
	pendingTotal := 0.0
	facMap := map[int64]map[string]any{}
	for _, b := range bills {
		fid, _ := strconv.ParseInt(fmt.Sprint(b["facilityId"]), 10, 64)
		isPaid := b["isPaid"] == true
		bal, _ := strconv.ParseFloat(fmt.Sprint(b["balanceDue"]), 64)
		if isPaid {
			paidCount++
		} else {
			pendingCount++
			pendingTotal += bal
		}
		if _, ok := facMap[fid]; !ok {
			facMap[fid] = map[string]any{
				"facilityId": fid, "facilityName": b["facilityName"], "facilityLogoUrl": b["facilityLogoUrl"],
				"pendingCount": 0, "pendingTotal": 0.0, "payUrl": "",
			}
		}
		if !isPaid {
			facMap[fid]["pendingCount"] = facMap[fid]["pendingCount"].(int) + 1
			facMap[fid]["pendingTotal"] = facMap[fid]["pendingTotal"].(float64) + bal
		}
	}
	facilities := []map[string]any{}
	for _, v := range facMap {
		facilities = append(facilities, v)
	}
	return true, "OK", map[string]any{
		"whatsappNo": wa, "bills": bills, "facilities": facilities,
		"pendingCount": pendingCount, "paidCount": paidCount, "pendingTotal": pendingTotal,
		"count": len(bills),
	}
}

func (s *Service) billRow(id, eid int64, btype string, amount, paid float64, month, due, status, plan, display string, fid int64, fname, logo, txn, method, phone string) map[string]any {
	bal := amount - paid
	isPaid := strings.EqualFold(status, "paid") || bal <= 0
	if display == "" {
		if strings.EqualFold(btype, "member") {
			display = "Member"
		} else {
			display = "Student"
		}
	}
	return map[string]any{
		"billId": id, "enrollmentId": eid, "type": btype, "audience": btype,
		"billAmount": amount, "paidAmount": paid, "balanceDue": bal,
		"billingMonth": month, "billingMonthLabel": month, "dueDate": due,
		"paymentStatus": status, "isPaid": isPaid, "canPayOnline": !isPaid,
		"paymentMethod": method, "transactionId": txn, "facilityId": fid,
		"facilityName": fname, "facilityLogoUrl": logo, "planName": plan, "displayName": display,
		"contactPhone": phone,
	}
}

func (s *Service) appendSyntheticMemberBills(ctx context.Context, last10 string, facilityID int64, typeFilter string, appendBill func(map[string]any)) {
	if typeFilter != "" && typeFilter != "member" {
		return
	}
	match := s.liveMemberPhoneMatchSQL()
	q := fmt.Sprintf(`
SELECT mship.id, mship.facility_id, COALESCE(m.full_name,''), COALESCE(mship.plan_name,''),
       COALESCE(mship.subscription_fee,0), COALESCE(mship.payment_status,'unpaid'),
       COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), ''), COALESCE(af.logo_url,''),
       COALESCE(NULLIF(m.whatsapp,''), m.contact_phone,'')
FROM memberships mship
JOIN members m ON m.id=mship.member_id
LEFT JOIN app_facilities af ON af.id=mship.facility_id
LEFT JOIN facilities f ON f.id=mship.facility_id
WHERE mship.status='active' AND %s=$1
  AND COALESCE(mship.subscription_fee,0) > 0
  AND LOWER(COALESCE(mship.payment_status,'unpaid')) <> 'paid'`, match)
	args := []any{last10}
	if facilityID > 0 {
		q += ` AND mship.facility_id=$2`
		args = append(args, facilityID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	month := time.Now().Format("2006-01")
	for rows.Next() {
		var mid, fid int64
		var name, plan, payStatus, fname, logo, phone string
		var fee float64
		_ = rows.Scan(&mid, &fid, &name, &plan, &fee, &payStatus, &fname, &logo, &phone)
		appendBill(s.billRow(8_000_000_000+mid, mid, "member", fee, 0, month, "", "pending", plan, name, fid, fname, logo, "", "", phone))
	}
}

func (s *Service) appendSyntheticStudentBills(ctx context.Context, last10 string, facilityID int64, typeFilter string, appendBill func(map[string]any)) {
	if typeFilter != "" && typeFilter != "student" {
		return
	}
	match := fmt.Sprintf("(%s=$1 OR %s=$1)", phoneLast10SQL("s.whatsapp"), phoneLast10SQL("s.contact_phone"))
	q := fmt.Sprintf(`
SELECT e.id, e.facility_id,
       TRIM(COALESCE(s.first_name,'')||' '||COALESCE(s.last_name,'')),
       COALESCE(e.plan_name,''), COALESCE(e.actual_fee,0),
       COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), ''), COALESCE(af.logo_url,''),
       COALESCE(NULLIF(s.whatsapp,''), s.contact_phone,'')
FROM student_enrollments e
JOIN students s ON s.id=e.student_id
LEFT JOIN app_facilities af ON af.id=e.facility_id
LEFT JOIN facilities f ON f.id=e.facility_id
WHERE e.status='active' AND %s AND COALESCE(e.actual_fee,0) > 0`, match)
	args := []any{last10}
	if facilityID > 0 {
		q += ` AND e.facility_id=$2`
		args = append(args, facilityID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	month := time.Now().Format("2006-01")
	for rows.Next() {
		var eid, fid int64
		var name, plan, fname, logo, phone string
		var fee float64
		_ = rows.Scan(&eid, &fid, &name, &plan, &fee, &fname, &logo, &phone)
		appendBill(s.billRow(7_000_000_000+eid, eid, "student", fee, 0, month, "", "pending", plan, name, fid, fname, logo, "", "", phone))
	}
}

func (s *Service) billingInitiate(ctx context.Context, body map[string]any) (bool, string, any) {
	uid, _ := parseInt64Field(body, "userId")
	fid, _ := parseInt64Field(body, "facilityId")
	amount, _ := strconv.ParseFloat(fmt.Sprint(body["amount"]), 64)
	billIDs := billIDsFromBody(body)
	if amount <= 0 {
		for _, id := range billIDs {
			if id >= 8_000_000_000 {
				var fee float64
				_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(subscription_fee,0) FROM memberships WHERE id=$1`, id-8_000_000_000).Scan(&fee)
				amount += fee
			} else if id >= 7_000_000_000 {
				var fee float64
				_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(actual_fee,0) FROM student_enrollments WHERE id=$1`, id-7_000_000_000).Scan(&fee)
				amount += fee
			} else {
				var bal float64
				_ = s.DB.QueryRowContext(ctx, `SELECT GREATEST(bill_amount-paid_amount,0) FROM fee_bills WHERE id=$1`, id).Scan(&bal)
				amount += bal
			}
		}
	}
	if amount <= 0 {
		return false, "No payable amount.", nil
	}
	orderID, keyID, amt, err := s.createPaymentOrder(ctx, amount, fmt.Sprintf("fee_%d_%d", uid, time.Now().Unix()), "FEES", map[string]any{
		"userId": uid, "facilityId": fid, "billIds": billIDs,
	})
	if err != nil && orderID == "" {
		return false, err.Error(), nil
	}
	return true, "OK", map[string]any{
		"orderId": orderID, "keyId": keyID, "amount": amt,
		"currency": "INR", "category": "FEES", "billIds": body["billIds"],
	}
}

func billIDsFromBody(body map[string]any) []int64 {
	out := []int64{}
	raw := body["billIds"]
	if raw == nil {
		raw = body["bill_ids"]
	}
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			if n := anyToInt64(x); n > 0 {
				out = append(out, n)
			}
		}
	case []float64:
		for _, x := range v {
			if n := int64(x); n > 0 {
				out = append(out, n)
			}
		}
	case []int64:
		out = append(out, v...)
	}
	return out
}

func anyToInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case json.Number:
		n, err := x.Int64()
		if err == nil {
			return n
		}
		f, err := x.Float64()
		if err == nil {
			return int64(f)
		}
		return 0
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

func (s *Service) billingConfirm(ctx context.Context, body map[string]any) (bool, string, any) {
	last10, _ := s.resolvePortalLast10(ctx, body)
	uid, _ := parseInt64Field(body, "userId")
	wa := s.resolvePortalWhatsApp(ctx, body)
	txn := strings.TrimSpace(fmt.Sprint(body["razorpayPaymentId"]))
	if txn == "" {
		txn = strings.TrimSpace(fmt.Sprint(body["transactionId"]))
	}
	if txn == "" {
		txn = fmt.Sprintf("manual_%d", time.Now().Unix())
	}
	s.ensureFeeBills(ctx)
	paid := 0
	for _, id := range billIDsFromBody(body) {
		if id >= 8_000_000_000 {
			mid := id - 8_000_000_000
			_, _ = s.DB.ExecContext(ctx, `UPDATE memberships SET payment_status='paid', updated_at=NOW() WHERE id=$1`, mid)
			var fid int64
			var fee float64
			var plan, name string
			_ = s.DB.QueryRowContext(ctx, `
SELECT ms.facility_id, COALESCE(ms.subscription_fee,0), COALESCE(ms.plan_name,''), COALESCE(m.full_name,'')
FROM memberships ms JOIN members m ON m.id=ms.member_id WHERE ms.id=$1`, mid).
				Scan(&fid, &fee, &plan, &name)
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO fee_bills (facility_id, user_id, contact_phone, bill_type, enrollment_id, bill_amount, paid_amount,
  billing_month, payment_status, plan_name, display_name, transaction_id, payment_method)
VALUES ($1,$2,$3,'member',$4,$5,$5,$6,'paid',$7,$8,$9,'razorpay')`,
				fid, uid, wa, mid, fee, time.Now().Format("2006-01"), plan, name, txn)
			paid++
			continue
		}
		if id >= 7_000_000_000 {
			eid := id - 7_000_000_000
			var fid int64
			var fee float64
			var plan, name string
			_ = s.DB.QueryRowContext(ctx, `
SELECT e.facility_id, COALESCE(e.actual_fee,0), COALESCE(e.plan_name,''),
       TRIM(COALESCE(s.first_name,'')||' '||COALESCE(s.last_name,''))
FROM student_enrollments e JOIN students s ON s.id=e.student_id WHERE e.id=$1`, eid).
				Scan(&fid, &fee, &plan, &name)
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO fee_bills (facility_id, user_id, contact_phone, bill_type, enrollment_id, bill_amount, paid_amount,
  billing_month, payment_status, plan_name, display_name, transaction_id, payment_method)
VALUES ($1,$2,$3,'student',$4,$5,$5,$6,'paid',$7,$8,$9,'razorpay')`,
				fid, uid, wa, eid, fee, time.Now().Format("2006-01"), plan, name, txn)
			paid++
			continue
		}
		res, err := s.DB.ExecContext(ctx, `
UPDATE fee_bills SET paid_amount=bill_amount, payment_status='paid', transaction_id=$2,
  payment_method='razorpay', updated_at=NOW()
WHERE id=$1`, id, txn)
		if err == nil {
			n, _ := res.RowsAffected()
			if n > 0 {
				paid++
			}
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_fee_bills SET paid_amount=bill_amount, payment_status='paid', transaction_id=$2
WHERE id=$1`, id, txn)
	}
	_ = last10
	return true, "Paid", map[string]any{"ok": true, "paidCount": paid, "transactionId": txn}
}

// billingSendPaymentReminder mirrors PHP billing_history.php payment_reminder_dns.
func (s *Service) billingSendPaymentReminder(ctx context.Context, body map[string]any) (bool, string, any) {
	ok, msg, data := s.billingMyBills(ctx, body)
	if !ok {
		return false, msg, nil
	}
	m, _ := data.(map[string]any)
	if m == nil {
		return false, "No bills", nil
	}
	wa := normalizeWA(fmt.Sprint(m["whatsappNo"]))
	if wa == "" {
		return false, "WhatsApp number required", nil
	}
	bills, _ := m["bills"].([]map[string]any)
	if bills == nil {
		if raw, ok := m["bills"].([]any); ok {
			for _, item := range raw {
				if bm, ok := item.(map[string]any); ok {
					bills = append(bills, bm)
				}
			}
		}
	}
	// Group unpaid by facility (PHP groups by phone).
	type agg struct {
		fid     int64
		names   []string
		amounts []string
		months  []string
	}
	byFac := map[int64]*agg{}
	for _, b := range bills {
		if b["isPaid"] == true {
			continue
		}
		bal, _ := strconv.ParseFloat(fmt.Sprint(b["balanceDue"]), 64)
		if bal <= 0 {
			continue
		}
		fid, _ := strconv.ParseInt(fmt.Sprint(b["facilityId"]), 10, 64)
		a := byFac[fid]
		if a == nil {
			a = &agg{fid: fid}
			byFac[fid] = a
		}
		name := strings.TrimSpace(fmt.Sprint(b["displayName"]))
		if name == "" {
			name = strings.TrimSpace(fmt.Sprint(b["planName"]))
		}
		if name == "" {
			name = "Member"
		}
		a.names = append(a.names, name)
		a.amounts = append(a.amounts, fmt.Sprintf("₹%.0f", bal))
		month := strings.TrimSpace(fmt.Sprint(b["billingMonth"]))
		if month == "" {
			month = "current"
		}
		a.months = append(a.months, month)
	}
	if len(byFac) == 0 {
		return true, "No pending bills to remind", map[string]any{"sent": 0}
	}
	sent := 0
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	for fid, a := range byFac {
		if base == "" {
			continue
		}
		names := strings.Join(a.names, ", ")
		amounts := strings.Join(a.amounts, ", ")
		months := strings.Join(a.months, ", ")
		btn := fmt.Sprintf("?p=f%d", fid)
		payload := map[string]any{
			"channel":     "both", // WhatsApp + push
			"facility_id": fid,
			"template":    "payment_reminder_dns",
			"to":          map[string]any{"whatsapp": wa},
			"data": map[string]any{
				"title":            "Payment reminder",
				"body":             fmt.Sprintf("Pending: %s · %s (%s)", names, amounts, months),
				"body_params":      []string{names, amounts, months},
				"button_url_param": btn,
				"type":             "payment_reminder",
			},
		}
		if err := s.postNotificationsSend(ctx, base, payload); err == nil {
			sent++
		}
	}
	return true, "Payment reminders sent.", map[string]any{"sent": sent, "facilities": len(byFac)}
}
