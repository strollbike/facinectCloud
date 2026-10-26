package members

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

type Plan struct {
	ID           int64    `json:"id"`
	FacilityID   int64    `json:"facility_id"`
	Name         string   `json:"name"`
	Price        *float64 `json:"price,omitempty"`
	DurationDays *int     `json:"duration_days,omitempty"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"created_at,omitempty"`
}

type CreatePlanInput struct {
	Name            string   `json:"name"`
	Price           *float64 `json:"price"`
	DurationDays    *int     `json:"duration_days"`
	SlotTime        string   `json:"slotTime"`
	SubFacilityName string   `json:"subFacilityName"`
}

type PlanSettings struct {
	PlanID     int64                  `json:"plan_id"`
	FacilityID int64                  `json:"facility_id"`
	Settings   map[string]interface{} `json:"settings"`
}

type MemberRequest struct {
	ID           int64  `json:"id"`
	FacilityID   int64  `json:"facility_id"`
	MemberID     *int64 `json:"member_id,omitempty"`
	MembershipID *int64 `json:"membership_id,omitempty"`
	FullName     string `json:"full_name,omitempty"`
	ContactPhone string `json:"contact_phone,omitempty"`
	RequestBox   string `json:"request_box,omitempty"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at,omitempty"`
	MinsPassed   int    `json:"mins_passed"`
}

type Coupon struct {
	ID         int64  `json:"id"`
	FacilityID int64  `json:"facility_id"`
	PlanID     *int64 `json:"plan_id,omitempty"`
	Code       string `json:"code"`
	Status     string `json:"status"`
}

type OpenSlot struct {
	ID          int64  `json:"id"`
	FacilityID  int64  `json:"facility_id"`
	SportID     *int64 `json:"sport_id,omitempty"`
	SubFacility string `json:"sub_facility,omitempty"`
	SlotTime    string `json:"slot_time,omitempty"`
	FromDate    string `json:"from_date,omitempty"`
	ToDate      string `json:"to_date,omitempty"`
	Status      string `json:"status"`
}

type OpenSlotInput struct {
	SportID     *int64 `json:"sport_id"`
	SubFacility string `json:"sub_facility"`
	SlotTime    string `json:"slot_time"`
	FromDate    string `json:"from_date"`
	ToDate      string `json:"to_date"`
}

type TeamInput struct {
	UserIDs  []int64                  `json:"userIds"`
	Members  []map[string]interface{} `json:"members"`
	TeamName string                   `json:"teamName"`
	Team     string                   `json:"team_name"`
}

type BulkGroupInput struct {
	Group     string  `json:"group"`
	TeamName  string  `json:"team_name"`
	MemberIDs []int64 `json:"member_ids"`
	PlanID    *int64  `json:"plan_id"`
	PlanName  string  `json:"plan_name"`
}

type GenerateCouponsInput struct {
	Count  int    `json:"count"`
	PlanID *int64 `json:"planId"`
	PlanId *int64 `json:"plan_id"`
}

type BulkNotifyInput struct {
	Message    string                   `json:"message"`
	Recipients []map[string]interface{} `json:"recipients"`
	SportID    string                   `json:"sport_id"`
}

type PaymentStatusInput struct {
	PaymentStatus string `json:"payment_status"`
	Paid          *bool  `json:"paid"`
}

type DecideRequestInput struct {
	Decision string `json:"decision"`
}

type ChangePlanInput struct {
	MemberID  int64  `json:"member_id"`
	PlanID    int64  `json:"plan_id"`
	OldPlanID int64  `json:"old_plan_id"`
	TeamName  string `json:"team_name"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	FeeType   string `json:"fee_type"`
	IsChange  bool   `json:"is_change"`
}

func (s *Service) ListPlans(ctx context.Context, facilityID int64) ([]Plan, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, facility_id, name, price, duration_days, status, created_at::text
FROM member_plans WHERE facility_id=$1 AND status='active' ORDER BY name`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var p Plan
		var price sql.NullFloat64
		var days sql.NullInt64
		if err := rows.Scan(&p.ID, &p.FacilityID, &p.Name, &price, &days, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		if price.Valid {
			v := price.Float64
			p.Price = &v
		}
		if days.Valid {
			v := int(days.Int64)
			p.DurationDays = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) CreatePlan(ctx context.Context, facilityID int64, in CreatePlanInput) (*Plan, error) {
	name := strings.TrimSpace(in.Name)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_plan")
	}
	if err := s.rejectDuplicatePlan(ctx, facilityID, 0, name, in.SubFacilityName, in.SlotTime); err != nil {
		return nil, err
	}
	var price, days interface{}
	if in.Price != nil {
		price = *in.Price
	}
	if in.DurationDays != nil {
		days = *in.DurationDays
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO member_plans (facility_id, name, price, duration_days, status)
VALUES ($1,$2,$3,$4,'active') RETURNING id`, facilityID, name, price, days).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetPlan(ctx, facilityID, id)
}

func (s *Service) GetPlan(ctx context.Context, facilityID, planID int64) (*Plan, error) {
	var p Plan
	var price sql.NullFloat64
	var days sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, name, price, duration_days, status, created_at::text
FROM member_plans WHERE id=$1 AND facility_id=$2`, planID, facilityID).
		Scan(&p.ID, &p.FacilityID, &p.Name, &price, &days, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	if price.Valid {
		v := price.Float64
		p.Price = &v
	}
	if days.Valid {
		v := int(days.Int64)
		p.DurationDays = &v
	}
	return &p, nil
}

// DeletePlan hard-removes the plan (and settings). Returns plan name so callers
// can free matching Membership booking blocks / slots.
func (s *Service) DeletePlan(ctx context.Context, facilityID, planID int64) (string, error) {
	var name string
	err := s.DB.QueryRowContext(ctx, `
SELECT name FROM member_plans WHERE id=$1 AND facility_id=$2`, planID, facilityID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("not_found")
	}
	if err != nil {
		return "", err
	}

	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM member_plan_settings WHERE plan_id=$1 AND facility_id=$2`, planID, facilityID)

	// Detach enrollments that reference this plan (keep member history rows).
	_, _ = s.DB.ExecContext(ctx, `
UPDATE memberships SET status='inactive', plan_id=NULL, updated_at=NOW()
WHERE facility_id=$1 AND (plan_id=$2 OR LOWER(TRIM(COALESCE(plan_name,''))) = LOWER(TRIM($3)))`,
		facilityID, planID, name)

	res, err := s.DB.ExecContext(ctx, `
DELETE FROM member_plans WHERE id=$1 AND facility_id=$2`, planID, facilityID)
	if err != nil {
		return name, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return name, fmt.Errorf("not_found")
	}
	return name, nil
}

func (s *Service) GetPlanSettings(ctx context.Context, facilityID, planID int64) (*PlanSettings, error) {
	if _, err := s.GetPlan(ctx, facilityID, planID); err != nil {
		return nil, fmt.Errorf("not_found")
	}
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT settings_json FROM member_plan_settings WHERE plan_id=$1 AND facility_id=$2`,
		planID, facilityID).Scan(&raw)
	settings := map[string]interface{}{}
	if err == sql.ErrNoRows {
		return &PlanSettings{PlanID: planID, FacilityID: facilityID, Settings: settings}, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(raw, &settings)
	if settings == nil {
		settings = map[string]interface{}{}
	}
	return &PlanSettings{PlanID: planID, FacilityID: facilityID, Settings: settings}, nil
}

func (s *Service) UpsertPlanSettings(ctx context.Context, facilityID, planID int64, settings map[string]interface{}) (*PlanSettings, error) {
	plan, err := s.GetPlan(ctx, facilityID, planID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if settings == nil {
		settings = map[string]interface{}{}
	}
	if err := s.rejectDuplicatePlan(
		ctx, facilityID, planID, plan.Name,
		settingText(settings, "subFacilityName"),
		settingText(settings, "slotTime"),
	); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("invalid_settings")
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO member_plan_settings (plan_id, facility_id, settings_json, updated_at)
VALUES ($1,$2,$3::jsonb,NOW())
ON CONFLICT (plan_id) DO UPDATE
SET settings_json=EXCLUDED.settings_json, facility_id=EXCLUDED.facility_id, updated_at=NOW()`,
		planID, facilityID, string(raw))
	if err != nil {
		return nil, err
	}
	return s.GetPlanSettings(ctx, facilityID, planID)
}

func settingText(settings map[string]interface{}, key string) string {
	if settings == nil {
		return ""
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func (s *Service) rejectDuplicatePlan(ctx context.Context, facilityID, exceptPlanID int64, name, court, slot string) error {
	nameKey := strings.ToLower(strings.TrimSpace(name))
	rows, err := s.DB.QueryContext(ctx, `
SELECT LOWER(TRIM(p.name)),
       COALESCE(s.settings_json->>'slotTime',''),
       COALESCE(s.settings_json->>'subFacilityName','')
FROM member_plans p
LEFT JOIN member_plan_settings s ON s.plan_id = p.id AND s.facility_id = p.facility_id
WHERE p.facility_id=$1 AND p.status='active' AND p.id<>$2`, facilityID, exceptPlanID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existingName, existingSlot, existingCourt string
		if err := rows.Scan(&existingName, &existingSlot, &existingCourt); err != nil {
			return err
		}
		if nameKey != "" && existingName == nameKey {
			return fmt.Errorf("duplicate_plan_name")
		}
		if planSlotsConflict(court, slot, existingCourt, existingSlot) {
			return fmt.Errorf("duplicate_plan_slot")
		}
	}
	return rows.Err()
}

func planSlotsConflict(court, slot, otherCourt, otherSlot string) bool {
	a := normalizePlanText(slot)
	b := normalizePlanText(otherSlot)
	if a == "" || b == "" || a != b {
		return false
	}
	ac := normalizePlanText(court)
	bc := normalizePlanText(otherCourt)
	if planCourtIsAll(ac) || planCourtIsAll(bc) {
		return true
	}
	return ac != "" && ac == bc
}

func planCourtIsAll(court string) bool {
	return court == "" || court == "all" || strings.Contains(court, "all sub")
}

func normalizePlanText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func (s *Service) UpdatePlanTeam(ctx context.Context, facilityID, planID int64, in TeamInput) (int, error) {
	plan, err := s.GetPlan(ctx, facilityID, planID)
	if err != nil {
		return 0, fmt.Errorf("not_found")
	}
	team := strings.TrimSpace(in.TeamName)
	if team == "" {
		team = strings.TrimSpace(in.Team)
	}
	ids := append([]int64{}, in.UserIDs...)
	for _, m := range in.Members {
		for _, key := range []string{"member_id", "memberId", "id", "user_id", "userId"} {
			if v, ok := m[key]; ok {
				switch n := v.(type) {
				case float64:
					ids = append(ids, int64(n))
				case int64:
					ids = append(ids, n)
				case json.Number:
					if i, err := n.Int64(); err == nil {
						ids = append(ids, i)
					}
				}
				break
			}
		}
	}
	if len(ids) == 0 && team == "" {
		return 0, fmt.Errorf("userIds_or_team_required")
	}
	updated := 0
	if len(ids) > 0 {
		for _, mid := range ids {
			res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET team_name=COALESCE(NULLIF($1,''), team_name),
  plan_id=$2, plan_name=$3, updated_at=NOW()
WHERE facility_id=$4 AND member_id=$5 AND status='active'`,
				team, plan.ID, plan.Name, facilityID, mid)
			if err != nil {
				return updated, err
			}
			n, _ := res.RowsAffected()
			updated += int(n)
		}
	} else {
		res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET team_name=$1, updated_at=NOW()
WHERE facility_id=$2 AND plan_id=$3 AND status='active'`,
			team, facilityID, planID)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		updated = int(n)
	}
	return updated, nil
}

func (s *Service) BulkUpdateGroup(ctx context.Context, facilityID int64, in BulkGroupInput) (int, error) {
	group := strings.TrimSpace(in.Group)
	if group == "" {
		group = strings.TrimSpace(in.TeamName)
	}
	if len(in.MemberIDs) == 0 {
		return 0, fmt.Errorf("member_ids_required")
	}
	var planName string
	var planID interface{}
	if in.PlanID != nil && *in.PlanID > 0 {
		p, err := s.GetPlan(ctx, facilityID, *in.PlanID)
		if err == nil {
			planName = p.Name
			planID = p.ID
		}
	}
	if planName == "" {
		planName = strings.TrimSpace(in.PlanName)
	}
	updated := 0
	for _, mid := range in.MemberIDs {
		res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET
  team_name=COALESCE(NULLIF($1,''), team_name),
  plan_name=COALESCE(NULLIF($2,''), plan_name),
  plan_id=COALESCE($3, plan_id),
  updated_at=NOW()
WHERE facility_id=$4 AND member_id=$5`,
			group, planName, planID, facilityID, mid)
		if err != nil {
			return updated, err
		}
		n, _ := res.RowsAffected()
		updated += int(n)
	}
	return updated, nil
}

func (s *Service) GenerateCoupons(ctx context.Context, facilityID int64, in GenerateCouponsInput) ([]Coupon, error) {
	count := in.Count
	if count <= 0 {
		count = 1
	}
	if count > 100 {
		count = 100
	}
	planID := in.PlanID
	if planID == nil {
		planID = in.PlanId
	}
	out := make([]Coupon, 0, count)
	for i := 0; i < count; i++ {
		code, err := stubCouponCode()
		if err != nil {
			return out, err
		}
		var pid interface{}
		if planID != nil {
			pid = *planID
		}
		var id int64
		err = s.DB.QueryRowContext(ctx, `
INSERT INTO member_coupons (facility_id, plan_id, code, status)
VALUES ($1,$2,$3,'active') RETURNING id`, facilityID, pid, code).Scan(&id)
		if err != nil {
			return out, err
		}
		c := Coupon{ID: id, FacilityID: facilityID, Code: code, Status: "active"}
		if planID != nil {
			c.PlanID = planID
		}
		out = append(out, c)
	}
	return out, nil
}

func stubCouponCode() (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return "MEM" + string(b), nil
}

type BulkNotifyResult struct {
	Queued     int    `json:"queued"`
	WhatsApp   int    `json:"whatsapp"`
	FcmPush    int    `json:"fcmPush"`
	AppPush    int    `json:"appPush"`
	NoAppUser  int    `json:"noAppUser"`
	NoFcmToken int    `json:"noFcmToken"`
	Message    string `json:"message"`
}

func (s *Service) BulkNotify(ctx context.Context, facilityID int64, in BulkNotifyInput) (*BulkNotifyResult, error) {
	phones := []string{}
	seenPhone := map[string]struct{}{}
	nameByPhone := map[string]string{}
	addPhone := func(raw, name string) {
		p := digitsOnlyPhone(raw)
		if p == "" {
			return
		}
		if strings.TrimSpace(name) != "" {
			nameByPhone[p] = strings.TrimSpace(name)
		}
		if _, ok := seenPhone[p]; ok {
			return
		}
		seenPhone[p] = struct{}{}
		phones = append(phones, p)
	}
	for _, r := range in.Recipients {
		phone := ""
		for _, k := range []string{"whatsapp", "whatsappNo", "phone", "contact_phone"} {
			if v, ok := r[k]; ok {
				phone = fmt.Sprint(v)
				break
			}
		}
		who := ""
		for _, k := range []string{"name", "full_name", "fullName", "memberName", "studentName"} {
			if v, ok := r[k]; ok {
				who = strings.TrimSpace(fmt.Sprint(v))
				if who != "" && who != "<nil>" {
					break
				}
				who = ""
			}
		}
		addPhone(phone, who)
	}
	if len(phones) == 0 {
		rows, err := s.DB.QueryContext(ctx, `
SELECT COALESCE(NULLIF(m.whatsapp,''), NULLIF(m.contact_phone,'')), COALESCE(m.full_name,'')
FROM memberships ms
JOIN members m ON m.id = ms.member_id
WHERE ms.facility_id=$1 AND ms.status='active'`, facilityID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var wa, full sql.NullString
			_ = rows.Scan(&wa, &full)
			if wa.Valid {
				addPhone(wa.String, full.String)
			}
		}
	}
	out := &BulkNotifyResult{}
	if len(phones) == 0 {
		out.Message = "No recipient phones."
		return out, nil
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = "Update from your facility on Facinect."
	}
	title := "Facility update"
	fname := s.facilityDisplayName(ctx, facilityID)
	if fname == "" {
		fname = "Facinect"
	}
	for _, phone := range phones {
		uid := s.resolveAppUserID(ctx, phone)
		gotAny := false
		if uid > 0 {
			if err := s.writeAppInbox(ctx, uid, facilityID, title, msg); err == nil {
				out.AppPush++
				gotAny = true
			}
			if err := s.notifyPush(ctx, facilityID, uid, title, msg); err == nil {
				out.FcmPush++
				gotAny = true
			} else {
				out.NoFcmToken++
			}
		} else {
			out.NoAppUser++
		}
		// facinect_facility_dynamic_msg: {{1}} facility, {{2}} this member, {{3}} message.
		// Push already sent above — WhatsApp-only here to avoid double FCM.
		who := nameByPhone[phone]
		if who == "" {
			who = "Member"
		}
		if err := s.notifyChannel(ctx, facilityID, phone, "facinect_facility_dynamic_msg", "whatsapp", map[string]interface{}{
			"language":    "en_US",
			"body_params": []string{fname, who, msg},
			"title":       title,
			"body":        msg,
		}); err == nil {
			out.WhatsApp++
			gotAny = true
		}
		if gotAny {
			out.Queued++
		}
	}
	out.Message = fmt.Sprintf(
		"Queued %d · app inbox %d · FCM %d · WhatsApp %d · no app user %d",
		out.Queued, out.AppPush, out.FcmPush, out.WhatsApp, out.NoAppUser,
	)
	return out, nil
}

func (s *Service) facilityDisplayName(ctx context.Context, facilityID int64) string {
	var name string
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(name,''), '') FROM app_facilities WHERE id=$1`, facilityID).Scan(&name)
	if name != "" {
		return name
	}
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(name,''), '') FROM facilities WHERE id=$1`, facilityID).Scan(&name)
	return name
}

func (s *Service) resolveAppUserID(ctx context.Context, phone string) int64 {
	phone = digitsOnlyPhone(phone)
	if phone == "" {
		return 0
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM app_users
WHERE status='active'
  AND regexp_replace(COALESCE(whatsapp_no,''), '\D', '', 'g') = $1
LIMIT 1`, phone).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

func (s *Service) writeAppInbox(ctx context.Context, userID, facilityID int64, title, body string) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, ntype, title, body, source, meta_json)
VALUES ($1,$2,'member_broadcast',$3,$4,'member','{"source":"member_bulk_notify"}'::jsonb)`,
		userID, facilityID, title, body)
	return err
}

func (s *Service) notifyPush(ctx context.Context, facilityID, userID int64, title, body string) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	payload := map[string]interface{}{
		"channel":     "push",
		"facility_id": facilityID,
		"template":    "member_broadcast",
		"to":          map[string]interface{}{"user_id": userID},
		"data": map[string]interface{}{
			"title": title,
			"body":  body,
			"type":  "member_broadcast",
		},
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
	// Treat failed job status as error when body includes it.
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

func digitsOnlyPhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 10 {
		d = "91" + d
	}
	return d
}

func (s *Service) notifyWhatsApp(ctx context.Context, facilityID int64, phone, template, body string) error {
	return s.notifyWhatsAppData(ctx, facilityID, phone, template, map[string]interface{}{
		"body": body, "message": body, "text": body, "title": "Facinect",
	})
}

// notifyWhatsAppData sends WhatsApp + push (phone resolves to app user / FCM).
func (s *Service) notifyWhatsAppData(ctx context.Context, facilityID int64, phone, template string, data map[string]interface{}) error {
	return s.notifyChannel(ctx, facilityID, phone, template, "both", data)
}

func (s *Service) notifyChannel(ctx context.Context, facilityID int64, phone, template, channel string, data map[string]interface{}) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	phone = digitsOnlyPhone(phone)
	if phone == "" {
		return fmt.Errorf("whatsapp_required")
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	ch := strings.ToLower(strings.TrimSpace(channel))
	if ch == "" {
		ch = "both"
	}
	// Ensure FCM has readable title/body when Meta template only has body_params.
	if ch == "both" || ch == "push" {
		if _, ok := data["title"]; !ok {
			data["title"] = "Facinect"
		}
		if _, ok := data["body"]; !ok {
			if msg, _ := data["message"].(string); strings.TrimSpace(msg) != "" {
				data["body"] = msg
			} else if text, _ := data["text"].(string); strings.TrimSpace(text) != "" {
				data["body"] = text
			} else {
				data["body"] = "You have a new update from your facility."
			}
		}
	}
	payload := map[string]interface{}{
		"channel":     ch,
		"facility_id": facilityID,
		"template":    template,
		"to":          map[string]string{"whatsapp": phone},
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
			errText = "notify_failed"
		}
		return fmt.Errorf("%s", errText)
	}
	return nil
}

// notifyMemberRegistrationConfirmation mirrors PHP member_edit.php (6 body params).
func (s *Service) notifyMemberRegistrationConfirmation(ctx context.Context, row *MemberRow) {
	if row == nil {
		return
	}
	phone := digitsOnlyPhone(row.WhatsApp)
	if phone == "" {
		phone = digitsOnlyPhone(row.Phone)
	}
	if phone == "" {
		return
	}
	facilityName := ""
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, row.FacilityID).Scan(&facilityName)
	if facilityName == "" {
		_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, row.FacilityID).Scan(&facilityName)
	}
	if facilityName == "" {
		facilityName = "Academy"
	}
	plan := strings.TrimSpace(row.PlanName)
	if plan == "" {
		plan = "Membership"
	}
	roleName := "Member"
	benefit := "You now have access to member features and account management services including exclusive member offers and advanced access features. To access your Member Portal, simply send \"Hi\" and select the option, then choose \"Member Portal\"."
	if row.PrimaryMember {
		roleName = "Primary Member"
		benefit = "As a Primary Member, you now have access to premium privileges including exclusive member offers, advanced access features, the ability to add additional members under this plan, and submit member removal requests when needed. To access your Member Portal, simply send \"Hi\" and select the Options, then choose \"Member Portal\"."
	}
	displayPhone := phone
	if len(displayPhone) > 10 {
		displayPhone = displayPhone[len(displayPhone)-10:]
	}
	_ = s.notifyWhatsAppData(ctx, row.FacilityID, phone, "member_registration_confirmation", map[string]interface{}{
		"title": "Welcome to " + facilityName,
		"body":  fmt.Sprintf("Hi %s, your %s registration is confirmed.", row.FullName, plan),
		"body_params": []string{
			row.FullName, plan, roleName, benefit, facilityName, displayPhone,
		},
		// Same phone as the admin app: normal priority stays in the tray.
		"android_priority": "normal",
	})
}

func (s *Service) ListOpenSlots(ctx context.Context, facilityID int64) ([]OpenSlot, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, facility_id, sport_id, COALESCE(sub_facility,''), COALESCE(slot_time,''),
       COALESCE(from_date::text,''), COALESCE(to_date::text,''), status
FROM member_open_slots WHERE facility_id=$1 AND status='open'
ORDER BY from_date NULLS LAST, id DESC`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenSlot{}
	for rows.Next() {
		var o OpenSlot
		var sport sql.NullInt64
		if err := rows.Scan(&o.ID, &o.FacilityID, &sport, &o.SubFacility, &o.SlotTime, &o.FromDate, &o.ToDate, &o.Status); err != nil {
			return nil, err
		}
		if sport.Valid {
			v := sport.Int64
			o.SportID = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Service) CreateOpenSlot(ctx context.Context, facilityID int64, in OpenSlotInput) (*OpenSlot, error) {
	var sport, from, to interface{}
	if in.SportID != nil {
		sport = *in.SportID
	}
	if strings.TrimSpace(in.FromDate) != "" {
		if err := validateDate(in.FromDate); err != nil {
			return nil, err
		}
		from = in.FromDate
	}
	if strings.TrimSpace(in.ToDate) != "" {
		if err := validateDate(in.ToDate); err != nil {
			return nil, err
		}
		to = in.ToDate
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO member_open_slots (facility_id, sport_id, sub_facility, slot_time, from_date, to_date, status)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5::date,$6::date,'open') RETURNING id`,
		facilityID, sport, strings.TrimSpace(in.SubFacility), strings.TrimSpace(in.SlotTime), from, to,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	list, err := s.ListOpenSlots(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return &OpenSlot{ID: id, FacilityID: facilityID, Status: "open"}, nil
}

func (s *Service) UpdatePaymentStatus(ctx context.Context, membershipID int64, status string) (*MemberRow, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if st == "" {
		return nil, fmt.Errorf("payment_status_required")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET payment_status=$1, updated_at=NOW() WHERE id=$2`, st, membershipID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.GetMembership(ctx, membershipID)
}

func (s *Service) BulkUpdatePayment(ctx context.Context, facilityID, planID int64, paid bool) (int, error) {
	st := "unpaid"
	if paid {
		st = "paid"
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE memberships SET payment_status=$1, updated_at=NOW()
WHERE facility_id=$2 AND plan_id=$3 AND status='active'`, st, facilityID, planID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func planSettingAmount(settings map[string]interface{}, key string) (float64, bool) {
	if settings == nil {
		return 0, false
	}
	raw, ok := settings[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		n, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		return n, err == nil
	}
}

func (s *Service) ChangeOrAddPlan(ctx context.Context, facilityID int64, in ChangePlanInput) (int, error) {
	plan, err := s.GetPlan(ctx, facilityID, in.PlanID)
	if err != nil {
		return 0, fmt.Errorf("plan_not_found")
	}
	if in.MemberID <= 0 {
		return 0, fmt.Errorf("member_id_required")
	}
	var start, end interface{}
	if strings.TrimSpace(in.StartDate) != "" {
		if err := validateDate(in.StartDate); err != nil {
			return 0, err
		}
		start = in.StartDate
	}
	if strings.TrimSpace(in.EndDate) != "" {
		if err := validateDate(in.EndDate); err != nil {
			return 0, err
		}
		end = in.EndDate
	}
	fee := 0.0
	if plan.Price != nil {
		fee = *plan.Price
	}
	if settings, err := s.GetPlanSettings(ctx, facilityID, plan.ID); err == nil && settings != nil {
		key := "indivPrice"
		ft := strings.ToLower(strings.TrimSpace(in.FeeType))
		if strings.Contains(ft, "grp") || strings.Contains(ft, "group") {
			key = "grpPrice"
		}
		if parsed, ok := planSettingAmount(settings.Settings, key); ok {
			fee = parsed
		}
	}
	var onPlan int
	if err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM memberships
WHERE facility_id=$1 AND member_id=$2 AND plan_id=$3 AND status='active'`,
		facilityID, in.MemberID, plan.ID).Scan(&onPlan); err != nil {
		return 0, err
	}
	if onPlan > 0 {
		return 0, fmt.Errorf("already_on_plan")
	}

	team := strings.TrimSpace(in.TeamName)
	if in.IsChange {
		q := `
UPDATE memberships SET
  plan_id=$1, plan_name=$2,
  team_name=COALESCE(NULLIF($3,''), team_name),
  start_date=COALESCE($4::date, start_date),
  end_date=COALESCE($5::date, end_date),
  subscription_fee=$6,
  updated_at=NOW()
WHERE facility_id=$7 AND member_id=$8 AND status='active'`
		args := []interface{}{plan.ID, plan.Name, team, start, end, fee, facilityID, in.MemberID}
		if in.OldPlanID > 0 {
			q += ` AND plan_id=$9`
			args = append(args, in.OldPlanID)
		}
		res, err := s.DB.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return 0, fmt.Errorf("membership_not_found")
		}
		return int(n), nil
	}

	var sport sql.NullInt64
	err = s.DB.QueryRowContext(ctx, `
SELECT sport_id FROM memberships
WHERE facility_id=$1 AND member_id=$2
ORDER BY id DESC LIMIT 1`, facilityID, in.MemberID).Scan(&sport)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("member_not_found")
	}
	if err != nil {
		return 0, err
	}
	var sportArg interface{}
	if sport.Valid {
		sportArg = sport.Int64
	}
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO memberships
  (facility_id, member_id, sport_id, plan_id, plan_name, team_name,
   start_date, end_date, subscription_fee, primary_member, status, payment_status)
VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7::date,$8::date,$9,false,'active','unpaid')`,
		facilityID, in.MemberID, sportArg, plan.ID, plan.Name, team, start, end, fee)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Service) ListRequests(ctx context.Context, facilityID int64) ([]MemberRequest, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, facility_id, member_id, membership_id, COALESCE(full_name,''), COALESCE(contact_phone,''),
       COALESCE(request_box,''), status, created_at::text,
       GREATEST(0, EXTRACT(EPOCH FROM (NOW() - created_at))/60)::int
FROM member_requests
WHERE facility_id=$1 AND status='pending'
ORDER BY created_at DESC LIMIT 200`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemberRequest{}
	for rows.Next() {
		var r MemberRequest
		var mid, mship sql.NullInt64
		if err := rows.Scan(
			&r.ID, &r.FacilityID, &mid, &mship, &r.FullName, &r.ContactPhone,
			&r.RequestBox, &r.Status, &r.CreatedAt, &r.MinsPassed,
		); err != nil {
			return nil, err
		}
		if mid.Valid {
			v := mid.Int64
			r.MemberID = &v
		}
		if mship.Valid {
			v := mship.Int64
			r.MembershipID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) GetRequest(ctx context.Context, requestID int64) (*MemberRequest, error) {
	var r MemberRequest
	var mid, mship sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, member_id, membership_id, COALESCE(full_name,''), COALESCE(contact_phone,''),
       COALESCE(request_box,''), status, created_at::text,
       GREATEST(0, EXTRACT(EPOCH FROM (NOW() - created_at))/60)::int
FROM member_requests WHERE id=$1`, requestID).Scan(
		&r.ID, &r.FacilityID, &mid, &mship, &r.FullName, &r.ContactPhone,
		&r.RequestBox, &r.Status, &r.CreatedAt, &r.MinsPassed,
	)
	if err != nil {
		return nil, err
	}
	if mid.Valid {
		v := mid.Int64
		r.MemberID = &v
	}
	if mship.Valid {
		v := mship.Int64
		r.MembershipID = &v
	}
	return &r, nil
}

func (s *Service) DecideRequest(ctx context.Context, requestID int64, decision string) (*MemberRequest, error) {
	d := strings.ToLower(strings.TrimSpace(decision))
	if d == "approve" {
		d = "approved"
	}
	if d == "reject" {
		d = "rejected"
	}
	if d != "approved" && d != "rejected" {
		return nil, fmt.Errorf("invalid_decision")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE member_requests SET status=$1, updated_at=NOW() WHERE id=$2 AND status='pending'`, d, requestID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	var r MemberRequest
	var mid, mship sql.NullInt64
	err = s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, member_id, membership_id, COALESCE(full_name,''), COALESCE(contact_phone,''),
       COALESCE(request_box,''), status, created_at::text, 0
FROM member_requests WHERE id=$1`, requestID).Scan(
		&r.ID, &r.FacilityID, &mid, &mship, &r.FullName, &r.ContactPhone,
		&r.RequestBox, &r.Status, &r.CreatedAt, &r.MinsPassed,
	)
	if err != nil {
		return nil, err
	}
	if mid.Valid {
		v := mid.Int64
		r.MemberID = &v
	}
	if mship.Valid {
		v := mship.Int64
		r.MembershipID = &v
	}
	if d == "approved" && r.MembershipID != nil {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE memberships SET status='active', updated_at=NOW() WHERE id=$1`, *r.MembershipID)
	}
	return &r, nil
}
