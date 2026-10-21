package students

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AttendancePushResult mirrors PHP AttendanceApi::notifyStudentAttendance.
type AttendancePushResult struct {
	OK            bool    `json:"ok"`
	AppPush       int     `json:"appPush"`
	FcmPush       int     `json:"fcmPush"`
	NoAppUser     int     `json:"noAppUser"`
	NoFcmToken    int     `json:"noFcmToken"`
	Message       string  `json:"message"`
	Reason        string  `json:"reason"`
	StudentID     int64   `json:"studentId"`
	PhoneMasked   string  `json:"phoneMasked"`
	MappedUserIDs []int64 `json:"mappedUserIds"`
	TargetUserIDs []int64 `json:"targetUserIds"`
}

func (s *Service) notifyStudentAttendance(ctx context.Context, facilityID, enrollmentID int64, date, status string) AttendancePushResult {
	empty := func(reason, msg string, studentID int64, phoneMasked string, mapped []int64) AttendancePushResult {
		if mapped == nil {
			mapped = []int64{}
		}
		return AttendancePushResult{
			OK: true, Message: msg, Reason: reason,
			StudentID: studentID, PhoneMasked: phoneMasked, MappedUserIDs: mapped,
			TargetUserIDs: []int64{},
		}
	}

	var studentID int64
	var firstName, lastName, phone, whatsapp sql.NullString
	err := s.DB.QueryRowContext(ctx, `
SELECT e.student_id,
       COALESCE(st.first_name,''), COALESCE(st.last_name,''),
       COALESCE(st.contact_phone,''), COALESCE(st.whatsapp,'')
FROM student_enrollments e
JOIN students st ON st.id = e.student_id
WHERE e.id=$1 AND e.facility_id=$2`, enrollmentID, facilityID).
		Scan(&studentID, &firstName, &lastName, &phone, &whatsapp)
	if err == sql.ErrNoRows {
		return empty("no_enrollment", "No student enrollment found", 0, "", nil)
	}
	if err != nil {
		return AttendancePushResult{OK: false, Reason: "exception", Message: err.Error(), MappedUserIDs: []int64{}, TargetUserIDs: []int64{}}
	}

	name := strings.TrimSpace(strings.TrimSpace(firstName.String) + " " + strings.TrimSpace(lastName.String))
	if name == "" {
		name = "Student"
	}
	rawPhone := strings.TrimSpace(whatsapp.String)
	if rawPhone == "" {
		rawPhone = strings.TrimSpace(phone.String)
	}
	phoneMasked := maskPhone(rawPhone)

	mapped := s.userIDsForStudentAudience(ctx, studentID)
	phoneUID := s.resolveAppUserIDByPhone(ctx, rawPhone)

	seen := map[int64]struct{}{}
	var targets []int64
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}
	for _, id := range mapped {
		add(id)
	}
	add(phoneUID)

	if len(targets) == 0 {
		msg := "Student WhatsApp empty and no audience_map — open Proximity with same phone"
		if rawPhone != "" {
			msg = fmt.Sprintf("Student phone %s has no Proximity login (same WhatsApp).", phoneMasked)
		}
		return empty("no_phone_no_map", msg, studentID, phoneMasked, mapped)
	}

	facilityName := s.facilityDisplayName(ctx, facilityID)
	title := facilityName
	if title == "" {
		title = "Attendance update"
	}
	statusLabel := strings.TrimSpace(status)
	if statusLabel == "" {
		statusLabel = "updated"
	}
	body := fmt.Sprintf("%s: attendance marked %s for %s.", name, statusLabel, date)

	out := AttendancePushResult{
		StudentID:     studentID,
		PhoneMasked:   phoneMasked,
		MappedUserIDs: mapped,
		TargetUserIDs: targets,
	}

	metaRaw, _ := json.Marshal(map[string]interface{}{
		"source":         "attendance",
		"enrollmentId":   enrollmentID,
		"studentId":      studentID,
		"attendanceDate": date,
		"status":         status,
		"audience":       "student",
	})

	for _, uid := range targets {
		pushErr := s.notifyPush(ctx, facilityID, uid, title, body, map[string]interface{}{
			"title":          title,
			"body":           body,
			"type":           "student_attendance",
			"enrollmentId":   fmt.Sprintf("%d", enrollmentID),
			"studentId":      fmt.Sprintf("%d", studentID),
			"attendanceDate": date,
			"status":         status,
			"audience":       "student",
		})
		if pushErr == nil {
			out.FcmPush++
			out.AppPush++ // notifications/send also writes inbox on FCM success
			continue
		}
		out.NoFcmToken++
		// App closed without token still gets inbox when they open Proximity.
		if err := s.writeAppInbox(ctx, uid, facilityID, title, body, string(metaRaw)); err == nil {
			out.AppPush++
		}
	}

	switch {
	case out.FcmPush > 0:
		out.OK = true
		out.Reason = "fcm_sent"
		out.Message = fmt.Sprintf("Push sent to %d device(s).", out.FcmPush)
	case out.AppPush > 0 && out.NoFcmToken > 0:
		out.OK = true
		out.Reason = "inbox_no_token"
		out.Message = "Inbox OK but no FCM token — open Proximity logged-in once to register push."
	case out.AppPush > 0:
		out.OK = true
		out.Reason = "inbox_only"
		out.Message = "Inbox notified; FCM not delivered."
	default:
		out.OK = false
		out.Reason = "no_target"
		out.Message = "No Proximity app user to notify."
		out.NoAppUser = 1
	}
	return out
}

func (s *Service) userIDsForStudentAudience(ctx context.Context, studentID int64) []int64 {
	if studentID <= 0 {
		return nil
	}
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_fcm_audience_map (
  id BIGSERIAL PRIMARY KEY,
  audience VARCHAR(32) NOT NULL,
  role_id BIGINT NOT NULL,
  users_user_id BIGINT NOT NULL,
  phone_norm VARCHAR(20),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (audience, role_id)
)`)
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT users_user_id FROM app_fcm_audience_map
WHERE audience='student' AND role_id=$1 AND users_user_id>0
LIMIT 50`, studentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	seen := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *Service) resolveAppUserIDByPhone(ctx context.Context, phone string) int64 {
	d := digitsOnly(phone)
	if len(d) < 10 {
		return 0
	}
	if len(d) == 10 {
		d = "91" + d
	}
	last10 := d
	if len(last10) > 10 {
		last10 = last10[len(last10)-10:]
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM app_users
WHERE status='active'
  AND (
    regexp_replace(COALESCE(whatsapp_no,''), '\D', '', 'g') = $1
    OR RIGHT(regexp_replace(COALESCE(whatsapp_no,''), '\D', '', 'g'), 10) = $2
    OR RIGHT(regexp_replace(COALESCE(phone,''), '\D', '', 'g'), 10) = $2
  )
LIMIT 1`, d, last10).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

func (s *Service) facilityDisplayName(ctx context.Context, facilityID int64) string {
	if facilityID <= 0 {
		return ""
	}
	var name string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, facilityID).Scan(&name)
	if err != nil || strings.TrimSpace(name) == "" {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, facilityID).Scan(&name)
	}
	return strings.TrimSpace(name)
}

func (s *Service) writeAppInbox(ctx context.Context, userID, facilityID int64, title, body, metaJSON string) error {
	if metaJSON == "" {
		metaJSON = "{}"
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, ntype, title, body, source, meta_json)
VALUES ($1,$2,'student_attendance',$3,$4,'student',$5::jsonb)`,
		userID, facilityID, title, body, metaJSON)
	return err
}

func (s *Service) notifyPush(ctx context.Context, facilityID, userID int64, title, body string, data map[string]interface{}) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	data["title"] = title
	data["body"] = body
	payload := map[string]interface{}{
		"channel":     "push",
		"facility_id": facilityID,
		"template":    "student_attendance",
		"to":          map[string]interface{}{"user_id": userID},
		"data":        data,
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
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

func digitsOnly(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeNotifyPhone(raw string) string {
	d := digitsOnly(raw)
	if len(d) == 10 {
		d = "91" + d
	}
	return d
}

// notifyStudentRegistrationConfirmation uses PHP member_registration_confirmation
// (same Meta template for new students).
func (s *Service) notifyStudentRegistrationConfirmation(ctx context.Context, row *StudentRow) {
	if row == nil {
		return
	}
	phone := normalizeNotifyPhone(row.WhatsApp)
	if phone == "" {
		phone = normalizeNotifyPhone(row.Phone)
	}
	if phone == "" {
		return
	}
	name := strings.TrimSpace(strings.TrimSpace(row.FirstName) + " " + strings.TrimSpace(row.LastName))
	if name == "" {
		name = "Student"
	}
	facilityName := s.facilityDisplayName(ctx, row.FacilityID)
	if facilityName == "" {
		facilityName = "Academy"
	}
	plan := strings.TrimSpace(row.PlanName)
	if plan == "" {
		plan = "Student plan"
	}
	roleName := "Student"
	benefit := "You are now enrolled for coaching. To access your Student Portal, simply send \"Hi\" and select Options, then choose \"Student Portal\"."
	displayPhone := phone
	if len(displayPhone) > 10 {
		displayPhone = displayPhone[len(displayPhone)-10:]
	}
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return
	}
	payload := map[string]interface{}{
		"channel":     "both", // WhatsApp + push
		"facility_id": row.FacilityID,
		"template":    "member_registration_confirmation",
		"to":          map[string]string{"whatsapp": phone},
		"data": map[string]interface{}{
			"title":       "Welcome to " + facilityName,
			"body":        fmt.Sprintf("Hi %s, your %s enrollment is confirmed.", name, plan),
			"body_params": []string{name, plan, roleName, benefit, facilityName, displayPhone},
			"type":        "student_registration",
		},
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	_, _ = io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

func maskPhone(raw string) string {
	d := digitsOnly(raw)
	if d == "" {
		return "(empty)"
	}
	if len(d) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(d)-4) + d[len(d)-4:]
}
