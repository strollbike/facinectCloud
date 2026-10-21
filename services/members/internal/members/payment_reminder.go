package members

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PaymentReminderSchedule is the UI-editable monthly auto-send timer.
type PaymentReminderSchedule struct {
	FacilityID  int64  `json:"facilityId"`
	Enabled     bool   `json:"enabled"`
	DayOfMonth  int    `json:"dayOfMonth"`
	HourIST     int    `json:"hourIst"`
	MinuteIST   int    `json:"minuteIst"`
	LastRunYM   string `json:"lastRunYm,omitempty"`
	Audience    string `json:"audience"` // both | member | student
}

type PaymentReminderRecipient struct {
	WhatsApp string  `json:"whatsapp"`
	Phone    string  `json:"phone"`
	Name     string  `json:"name"`
	Amount   float64 `json:"amount"`
	Month    string  `json:"month"`
	UserID   int64   `json:"user_id"`
}

type PaymentReminderInput struct {
	Recipients []PaymentReminderRecipient `json:"recipients"`
	Audience   string                     `json:"audience"` // member | student
}

type PaymentReminderResult struct {
	Queued    int    `json:"queued"`
	WhatsApp  int    `json:"whatsapp"`
	AppPush   int    `json:"appPush"`
	FcmPush   int    `json:"fcmPush"`
	NoAppUser int    `json:"noAppUser"`
	Message   string `json:"message"`
}

func (s *Service) EnsurePaymentReminderSchema(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS facility_payment_reminder_schedule (
  facility_id   BIGINT NOT NULL,
  enabled       BOOLEAN NOT NULL DEFAULT TRUE,
  day_of_month  INT NOT NULL DEFAULT 4 CHECK (day_of_month BETWEEN 1 AND 28),
  hour_ist      INT NOT NULL DEFAULT 2 CHECK (hour_ist BETWEEN 0 AND 23),
  minute_ist    INT NOT NULL DEFAULT 0 CHECK (minute_ist BETWEEN 0 AND 59),
  audience      VARCHAR(16) NOT NULL DEFAULT 'member',
  last_run_ym   VARCHAR(7) NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	if err != nil {
		return err
	}
	// Already on composite PK (facility_id, audience)?
	var keyCols int
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.key_column_usage
WHERE table_schema='public'
  AND table_name='facility_payment_reminder_schedule'
  AND constraint_name IN (
    SELECT constraint_name FROM information_schema.table_constraints
    WHERE table_schema='public'
      AND table_name='facility_payment_reminder_schedule'
      AND constraint_type='PRIMARY KEY'
  )`).Scan(&keyCols)
	if keyCols >= 2 {
		return nil
	}
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS facility_payment_reminder_schedule_v2 (
  facility_id   BIGINT NOT NULL,
  enabled       BOOLEAN NOT NULL DEFAULT TRUE,
  day_of_month  INT NOT NULL DEFAULT 4 CHECK (day_of_month BETWEEN 1 AND 28),
  hour_ist      INT NOT NULL DEFAULT 2 CHECK (hour_ist BETWEEN 0 AND 23),
  minute_ist    INT NOT NULL DEFAULT 0 CHECK (minute_ist BETWEEN 0 AND 59),
  audience      VARCHAR(16) NOT NULL CHECK (audience IN ('member','student')),
  last_run_ym   VARCHAR(7) NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (facility_id, audience)
)`)
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule_v2
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, last_run_ym, updated_at)
SELECT facility_id, enabled, day_of_month, hour_ist, minute_ist, 'member', last_run_ym, updated_at
FROM facility_payment_reminder_schedule
WHERE audience IN ('both','member')
ON CONFLICT (facility_id, audience) DO NOTHING`)
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule_v2
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, last_run_ym, updated_at)
SELECT facility_id, enabled, day_of_month, hour_ist, minute_ist, 'student', last_run_ym, updated_at
FROM facility_payment_reminder_schedule
WHERE audience IN ('both','student')
ON CONFLICT (facility_id, audience) DO NOTHING`)
	_, _ = s.DB.ExecContext(ctx, `DROP TABLE IF EXISTS facility_payment_reminder_schedule`)
	_, err = s.DB.ExecContext(ctx, `
ALTER TABLE IF EXISTS facility_payment_reminder_schedule_v2
  RENAME TO facility_payment_reminder_schedule`)
	return err
}

func billingTZ() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.Local
	}
	return loc
}

func normalizeReminderAudience(raw string) string {
	a := strings.ToLower(strings.TrimSpace(raw))
	if a == "student" {
		return "student"
	}
	return "member"
}

func (s *Service) GetPaymentReminderSchedule(ctx context.Context, facilityID int64, audience string) (PaymentReminderSchedule, error) {
	_ = s.EnsurePaymentReminderSchema(ctx)
	audience = normalizeReminderAudience(audience)
	out := PaymentReminderSchedule{
		FacilityID: facilityID,
		Enabled:    true,
		DayOfMonth: 4,
		HourIST:    2,
		MinuteIST:  0,
		Audience:   audience,
	}
	var enabled bool
	var day, hour, minute int
	var aud, last sql.NullString
	err := s.DB.QueryRowContext(ctx, `
SELECT enabled, day_of_month, hour_ist, minute_ist, audience, last_run_ym
FROM facility_payment_reminder_schedule WHERE facility_id=$1 AND audience=$2`, facilityID, audience).
		Scan(&enabled, &day, &hour, &minute, &aud, &last)
	if err == sql.ErrNoRows {
		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, updated_at)
VALUES ($1, TRUE, 4, 2, 0, $2, NOW())
ON CONFLICT (facility_id, audience) DO NOTHING`, facilityID, audience)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = enabled
	out.DayOfMonth = day
	out.HourIST = hour
	out.MinuteIST = minute
	out.Audience = audience
	if last.Valid {
		out.LastRunYM = last.String
	}
	return out, nil
}

func (s *Service) SetPaymentReminderSchedule(ctx context.Context, facilityID int64, in PaymentReminderSchedule) (PaymentReminderSchedule, error) {
	_ = s.EnsurePaymentReminderSchema(ctx)
	day := in.DayOfMonth
	if day < 1 {
		day = 4
	}
	if day > 28 {
		day = 28
	}
	hour := in.HourIST
	if hour < 0 || hour > 23 {
		hour = 2
	}
	minute := in.MinuteIST
	if minute < 0 || minute > 59 {
		minute = 0
	}
	audience := normalizeReminderAudience(in.Audience)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,NOW())
ON CONFLICT (facility_id, audience) DO UPDATE SET
  enabled=EXCLUDED.enabled,
  day_of_month=EXCLUDED.day_of_month,
  hour_ist=EXCLUDED.hour_ist,
  minute_ist=EXCLUDED.minute_ist,
  updated_at=NOW()`, facilityID, in.Enabled, day, hour, minute, audience)
	if err != nil {
		return PaymentReminderSchedule{}, err
	}
	return s.GetPaymentReminderSchedule(ctx, facilityID, audience)
}

// SendPaymentReminders: app push + WhatsApp payment_reminder_dns for selected recipients.
func (s *Service) SendPaymentReminders(ctx context.Context, facilityID int64, in PaymentReminderInput) (*PaymentReminderResult, error) {
	audience := strings.ToLower(strings.TrimSpace(in.Audience))
	if audience == "" {
		audience = "member"
	}
	out := &PaymentReminderResult{}
	if len(in.Recipients) == 0 {
		out.Message = "No recipients."
		return out, nil
	}
	lines := s.reminderLines(ctx, facilityID, audience, in.Recipients)
	if len(lines) == 0 {
		out.Message = "No recipients."
		return out, nil
	}
	// Same grouping as Facinect billing_history.php: one WhatsApp per phone.
	batches := groupReminderLines(lines)
	for _, batch := range batches {
		if len(batch) == 0 {
			continue
		}
		title := "Payment reminder"
		seen := map[string]bool{}
		for _, r := range batch {
		phone := r.phone
		if phone == "" {
			continue
		}
		key := phoneKey(phone)
		if seen[key] {
			continue
		}
		seen[key] = true
		label, amounts, months := paymentReminderParams(audience, batch)
		body := paymentReminderText(label, amounts, months, r.payURL)

		uid := r.userID
		if uid <= 0 {
			uid = s.resolveAppUserID(ctx, phone)
		}
		got := false
		payURL := r.payURL
		if uid > 0 {
			if err := s.writeBillingInbox(ctx, uid, facilityID, title, body, audience); err == nil {
				out.AppPush++
				got = true
			}
			if err := s.notifyBillingPush(ctx, facilityID, uid, title, body, audience, payURL); err == nil {
				out.FcmPush++
				got = true
			}
		} else {
			out.NoAppUser++
		}
		// payment_reminder_dns (en), same as Facinect billing_history.php:
		// {{1}} Student/Member: names, {{2}} amounts, {{3}} months, button ?p=encrypted.
		btn := "?p=" + encryptURLParam(
			fmt.Sprintf("contact_phone=%s&facilityId=%d", phoneKey(phone), facilityID),
			s.Cfg.EncryptionKey,
		)
		if err := s.notifyChannel(ctx, facilityID, phone, "payment_reminder_dns", "whatsapp", map[string]interface{}{
			"language":         "en",
			"body_params":      []string{label, amounts, months},
			"button_url_param": btn,
			"title":            title,
			"body":             body,
			"message":          body,
		}); err == nil {
			out.WhatsApp++
			got = true
		}
		if got {
			out.Queued++
		}
		}
	}
	out.Message = fmt.Sprintf(
		"Queued %d · app %d · FCM %d · WhatsApp %d · no app user %d",
		out.Queued, out.AppPush, out.FcmPush, out.WhatsApp, out.NoAppUser,
	)
	return out, nil
}

func (s *Service) writeBillingInbox(ctx context.Context, userID, facilityID int64, title, body, audience string) error {
	fname := ""
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(name,''), '') FROM app_facilities WHERE id=$1`, facilityID).Scan(&fname)
	if fname == "" {
		_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(name,''), '') FROM facilities WHERE id=$1`, facilityID).Scan(&fname)
	}
	phone10 := ""
	var wa sql.NullString
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(whatsapp_no,''), NULLIF(phone,''), '') FROM app_users WHERE id=$1`, userID).Scan(&wa)
	if wa.Valid {
		d := digitsOnlyPhone(wa.String)
		if len(d) >= 10 {
			phone10 = d[len(d)-10:]
		}
	}
	payURL := ""
	if phone10 != "" && facilityID > 0 {
		payURL = fmt.Sprintf(
			"https://facinect.com/Services/Facinect/whatsapp/Booking/student_payment.php?contact_phone=%s&facilityId=%d",
			phone10, facilityID,
		)
	}
	meta := fmt.Sprintf(
		`{"source":"billing_reminder","audience":%q,"facilityId":%d,"facilityName":%q,"requiresPayment":true,"payAction":"fees","payUrl":%q,"isPaid":false}`,
		audience, facilityID, fname, payURL,
	)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, ntype, title, body, facility_name, source, meta_json)
VALUES ($1,$2,'billing_reminder',$3,$4,NULLIF($5,''),'billing',$6::jsonb)`,
		userID, facilityID, title, body, fname, meta)
	return err
}

func (s *Service) notifyBillingPush(ctx context.Context, facilityID, userID int64, title, body, audience, payURL string) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	data := map[string]interface{}{
		"title":           title,
		"body":            body,
		"type":            "billing_reminder",
		"ntype":           "billing_reminder",
		"audience":        audience,
		"facilityId":      fmt.Sprintf("%d", facilityID),
		"requiresPayment": "true",
		"payAction":       "fees",
		"click_action":    "FLUTTER_NOTIFICATION_CLICK",
	}
	if payURL != "" {
		data["payUrl"] = payURL
	}
	payload := map[string]interface{}{
		"channel":     "push",
		"facility_id": facilityID,
		"template":    "billing_reminder",
		"to":          map[string]interface{}{"user_id": userID},
		"data":        data,
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var job map[string]interface{}
	_ = json.Unmarshal(b, &job)
	if st, _ := job["status"].(string); strings.EqualFold(st, "failed") {
		errText, _ := job["error_text"].(string)
		if errText == "" {
			errText = "push_failed"
		}
		return fmt.Errorf("%s", errText)
	}
	return nil
}

// RunDuePaymentReminderSchedules sends monthly reminders for facilities due now (IST).
func (s *Service) RunDuePaymentReminderSchedules(ctx context.Context) {
	_ = s.EnsurePaymentReminderSchema(ctx)
	now := time.Now().In(billingTZ())
	ym := now.Format("2006-01")
	day := now.Day()
	hour := now.Hour()
	minute := now.Minute()

	rows, err := s.DB.QueryContext(ctx, `
SELECT facility_id, audience FROM facility_payment_reminder_schedule
WHERE enabled=TRUE
  AND day_of_month=$1 AND hour_ist=$2 AND minute_ist=$3
  AND (last_run_ym IS NULL OR last_run_ym <> $4)`, day, hour, minute, ym)
	if err != nil {
		log.Printf("payment reminder schedule query: %v", err)
		return
	}
	defer rows.Close()

	type job struct {
		fid      int64
		audience string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if rows.Scan(&j.fid, &j.audience) == nil {
			jobs = append(jobs, j)
		}
	}
	for _, j := range jobs {
		s.runFacilityMonthlyReminders(ctx, j.fid, j.audience)
		_, _ = s.DB.ExecContext(ctx, `
UPDATE facility_payment_reminder_schedule SET last_run_ym=$2, updated_at=NOW()
WHERE facility_id=$1 AND audience=$3`, j.fid, ym, normalizeReminderAudience(j.audience))
	}
}

func (s *Service) runFacilityMonthlyReminders(ctx context.Context, facilityID int64, audience string) {
	audience = normalizeReminderAudience(audience)
	if audience == "member" {
		recs := s.pendingMemberReminderRecipients(ctx, facilityID)
		if len(recs) > 0 {
			_, _ = s.SendPaymentReminders(ctx, facilityID, PaymentReminderInput{
				Audience: "member", Recipients: recs,
			})
		}
		return
	}
	recs := s.pendingStudentReminderRecipients(ctx, facilityID)
	if len(recs) > 0 {
		_, _ = s.SendPaymentReminders(ctx, facilityID, PaymentReminderInput{
			Audience: "student", Recipients: recs,
		})
	}
}

func (s *Service) pendingMemberReminderRecipients(ctx context.Context, facilityID int64) []PaymentReminderRecipient {
	out := []PaymentReminderRecipient{}
	// Prefer fee_bills when present.
	rows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(contact_phone,''), COALESCE(display_name,''),
       COALESCE(bill_amount,0),
       COALESCE(NULLIF(billing_month,''), to_char(due_date,'YYYY-MM-DD'), '')
FROM fee_bills
WHERE facility_id=$1 AND LOWER(bill_type)='member'
  AND LOWER(payment_status) IN ('pending','unpaid')`, facilityID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var phone, name, month string
			var amt float64
			if rows.Scan(&phone, &name, &amt, &month) != nil {
				continue
			}
			out = append(out, PaymentReminderRecipient{Phone: phone, Name: name, Amount: amt, Month: month})
		}
		if len(out) > 0 {
			return out
		}
	}
	// Fallback: active memberships.
	mrows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(NULLIF(m.whatsapp,''), NULLIF(m.contact_phone,'')),
       COALESCE(NULLIF(m.full_name,''), 'Member'),
       COALESCE(ms.subscription_fee,0)
FROM memberships ms
JOIN members m ON m.id = ms.member_id
WHERE ms.facility_id=$1 AND ms.status='active'
  AND LOWER(COALESCE(ms.payment_status,'unpaid')) IN ('unpaid','pending')`, facilityID)
	if err != nil {
		return out
	}
	defer mrows.Close()
	for mrows.Next() {
		var phone, name string
		var amt float64
		if mrows.Scan(&phone, &name, &amt) != nil {
			continue
		}
		out = append(out, PaymentReminderRecipient{Phone: phone, Name: name, Amount: amt})
	}
	return out
}

func (s *Service) pendingStudentReminderRecipients(ctx context.Context, facilityID int64) []PaymentReminderRecipient {
	out := []PaymentReminderRecipient{}
	rows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(contact_phone,''), COALESCE(display_name,''),
       COALESCE(bill_amount,0),
       COALESCE(NULLIF(billing_month,''), to_char(due_date,'YYYY-MM-DD'), '')
FROM fee_bills
WHERE facility_id=$1 AND LOWER(bill_type)='student'
  AND LOWER(payment_status) IN ('pending','unpaid')`, facilityID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var phone, name, month string
			var amt float64
			if rows.Scan(&phone, &name, &amt, &month) != nil {
				continue
			}
			out = append(out, PaymentReminderRecipient{Phone: phone, Name: name, Amount: amt, Month: month})
		}
		if len(out) > 0 {
			return out
		}
	}
	srows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(NULLIF(st.whatsapp,''), NULLIF(st.contact_phone,'')),
       TRIM(CONCAT(COALESCE(st.first_name,''),' ',COALESCE(st.last_name,''))),
       COALESCE(se.actual_fee,0)
FROM student_enrollments se
JOIN students st ON st.id = se.student_id
WHERE se.facility_id=$1 AND se.status='active'`, facilityID)
	if err != nil {
		return out
	}
	defer srows.Close()
	for srows.Next() {
		var phone, name string
		var amt float64
		if srows.Scan(&phone, &name, &amt) != nil {
			continue
		}
		out = append(out, PaymentReminderRecipient{Phone: phone, Name: name, Amount: amt})
	}
	return out
}

// StartPaymentReminderCron ticks every minute and runs due schedules.
func (s *Service) StartPaymentReminderCron() {
	go func() {
		_ = s.EnsurePaymentReminderSchema(context.Background())
		// Seed schedules for facilities that already have members/students.
		s.seedMissingSchedules(context.Background())
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			s.RunDuePaymentReminderSchedules(ctx)
			cancel()
		}
	}()
	log.Printf("payment reminder cron started (IST schedule, default day=4 hour=2)")
}

func (s *Service) seedMissingSchedules(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, updated_at)
SELECT DISTINCT facility_id, TRUE, 4, 2, 0, 'member', NOW()
FROM memberships WHERE status='active'
ON CONFLICT (facility_id, audience) DO NOTHING`)
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_payment_reminder_schedule
  (facility_id, enabled, day_of_month, hour_ist, minute_ist, audience, updated_at)
SELECT DISTINCT facility_id, TRUE, 4, 2, 0, 'student', NOW()
FROM student_enrollments WHERE status='active'
ON CONFLICT (facility_id, audience) DO NOTHING`)
}

// ParseHourMinute accepts "2:00", "02:00", "14:30".
func ParseHourMinute(raw string) (hour, minute int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, false
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 1 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m := 0
	if len(parts) > 1 {
		m, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || m < 0 || m > 59 {
			return 0, 0, false
		}
	}
	return h, m, true
}

type reminderLine struct {
	phone  string
	name   string
	month  string
	payURL string
	amount float64
	userID int64
}

func (s *Service) reminderLines(ctx context.Context, facilityID int64, audience string, recipients []PaymentReminderRecipient) []reminderLine {
	out := []reminderLine{}
	for _, r := range recipients {
		phone := digitsOnlyPhone(r.WhatsApp)
		if phone == "" {
			phone = digitsOnlyPhone(r.Phone)
		}
		if phone == "" {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			if audience == "student" {
				name = "Student"
			} else {
				name = "Member"
			}
		}
		bills := s.lookupUnpaidBills(ctx, facilityID, audience, phone)
		if len(bills) > 0 {
			for i := range bills {
				if bills[i].name == "" {
					bills[i].name = name
				}
				if bills[i].userID == 0 {
					bills[i].userID = r.UserID
				}
			}
			out = append(out, bills...)
			continue
		}
		out = append(out, reminderLine{
			phone:  phone,
			name:   name,
			month:  strings.TrimSpace(r.Month),
			payURL: payLink(phone, facilityID),
			amount: r.Amount,
			userID: r.UserID,
		})
	}
	return out
}

func (s *Service) lookupUnpaidBills(ctx context.Context, facilityID int64, audience, phone string) []reminderLine {
	key := phoneKey(phone)
	if key == "" || facilityID <= 0 {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(contact_phone,''), COALESCE(display_name,''),
       COALESCE(bill_amount,0),
       COALESCE(NULLIF(billing_month,''), to_char(due_date,'YYYY-MM-DD'), '')
FROM fee_bills
WHERE facility_id=$1 AND LOWER(bill_type)=$2
  AND LOWER(payment_status) IN ('pending','unpaid')
  AND RIGHT(regexp_replace(COALESCE(contact_phone,''), '[^0-9]', '', 'g'), 10)=$3
ORDER BY due_date NULLS LAST, id`,
		facilityID, audience, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []reminderLine
	for rows.Next() {
		var line reminderLine
		if rows.Scan(&line.phone, &line.name, &line.amount, &line.month) != nil {
			continue
		}
		if digitsOnlyPhone(line.phone) == "" {
			line.phone = phone
		}
		line.payURL = payLink(line.phone, facilityID)
		out = append(out, line)
	}
	return out
}

func groupReminderLines(lines []reminderLine) [][]reminderLine {
	order := []string{}
	groups := map[string][]reminderLine{}
	for _, line := range lines {
		key := phoneKey(line.phone)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], line)
	}
	out := make([][]reminderLine, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func paymentReminderParams(audience string, lines []reminderLine) (label, amounts, months string) {
	role := "Member"
	if audience == "student" {
		role = "Student"
	}
	label = role + ": " + joinWithAnd(uniqueNames(lines))
	amounts = joinWithAnd(uniqueAmounts(lines))
	months = strings.Join(uniqueDates(lines), ", ")
	if months == "" {
		months = time.Now().In(billingTZ()).Format("2006-01-02")
	}
	return label, amounts, months
}

func paymentReminderText(label, amounts, months, payURL string) string {
	text := fmt.Sprintf("Dear %s,\nThis is a reminder. Your Rs. %s for the month of %s is pending.\nYou may follow the link below to make the payment.",
		label, amounts, months)
	if payURL != "" {
		text += "\n" + payURL
	}
	return text
}

func uniqueNames(lines []reminderLine) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range lines {
		name := strings.TrimSpace(line.name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"Member"}
	}
	return out
}

func uniqueAmounts(lines []reminderLine) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range lines {
		label := fmt.Sprintf("₹%.0f", line.amount)
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	if len(out) == 0 {
		return []string{"₹0"}
	}
	return out
}

func uniqueDates(lines []reminderLine) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range lines {
		d := normalizeBillDate(line.month)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func normalizeBillDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) >= 10 && raw[4] == '-' && raw[7] == '-' {
		return raw[:10]
	}
	if len(raw) == 7 && raw[4] == '-' {
		return raw + "-01"
	}
	for _, layout := range []string{"January 2006", "Jan 2006", "02 Jan 2006", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return raw
}

func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " & " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " & " + items[len(items)-1]
	}
}

func phoneKey(phone string) string {
	d := digitsOnlyPhone(phone)
	if len(d) >= 10 {
		return d[len(d)-10:]
	}
	return d
}

// encryptURLParam matches Facinect PHP encrypt_url_param (AES-256-CBC, openssl options 0).
func encryptURLParam(plain, key string) string {
	k := make([]byte, 32)
	copy(k, []byte(key))
	block, err := aes.NewCipher(k)
	if err != nil {
		return ""
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return ""
	}
	src := pkcs7Pad([]byte(plain), aes.BlockSize)
	dst := make([]byte, len(src))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(dst, src)
	packed := append([]byte(base64.StdEncoding.EncodeToString(dst)+"::"), iv...)
	out := base64.StdEncoding.EncodeToString(packed)
	return strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(out)
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	pad := blockSize - (len(src) % blockSize)
	out := make([]byte, len(src)+pad)
	copy(out, src)
	for i := len(src); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func payLink(phone string, facilityID int64) string {
	key := phoneKey(phone)
	if key == "" || facilityID <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"https://facinect.com/Services/Facinect/whatsapp/Booking/student_payment.php?contact_phone=%s&facilityId=%d",
		key, facilityID,
	)
}
