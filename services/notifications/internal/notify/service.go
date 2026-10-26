package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/facinect/notifications/internal/config"
)

type Service struct {
	DB   *sql.DB
	Cfg  config.Config
	WA   *WhatsAppClient
	Push *PushClient
}

type SendRequest struct {
	Channel     string                 `json:"channel"`
	FacilityID  *int64                 `json:"facility_id"`
	TemplateKey string                 `json:"template"`
	To          SendTo                 `json:"to"`
	Data        map[string]interface{} `json:"data"`
	Priority    string                 `json:"priority"`
}

type SendTo struct {
	WhatsApp     string   `json:"whatsapp"`
	UserID       *int64   `json:"user_id"`
	DeviceTokens []string `json:"device_tokens"`
	Name         string   `json:"name,omitempty"`
}

type Job struct {
	ID          int64           `json:"id"`
	FacilityID  *int64          `json:"facility_id,omitempty"`
	Channel     string          `json:"channel"`
	TemplateKey string          `json:"template_key,omitempty"`
	ToWhatsApp  string          `json:"to_whatsapp,omitempty"`
	ToUserID    *int64          `json:"to_user_id,omitempty"`
	Status      string          `json:"status"`
	ProviderRef string          `json:"provider_ref,omitempty"`
	ErrorText   string          `json:"error_text,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (s *Service) Send(ctx context.Context, req SendRequest) (*Job, error) {
	ch := strings.ToLower(strings.TrimSpace(req.Channel))
	if ch == "" {
		ch = "whatsapp"
	}
	if ch != "whatsapp" && ch != "push" && ch != "both" {
		return nil, fmt.Errorf("invalid_channel")
	}
	wa := normalizePhone(req.To.WhatsApp)
	if (ch == "whatsapp" || ch == "both") && wa == "" && req.To.UserID == nil {
		return nil, fmt.Errorf("whatsapp_or_user_required")
	}
	// Push may resolve later via user_id or WhatsApp → app_users (Facinect NotifyDispatch).
	if (ch == "push" || ch == "both") &&
		len(req.To.DeviceTokens) == 0 &&
		req.To.UserID == nil &&
		strings.TrimSpace(req.To.WhatsApp) == "" {
		return nil, fmt.Errorf("device_token_or_user_or_whatsapp_required")
	}

	payload := map[string]interface{}{
		"template": req.TemplateKey,
		"data":     req.Data,
		"priority": req.Priority,
		"to":       req.To,
	}
	raw, _ := json.Marshal(payload)

	var facility interface{}
	if req.FacilityID != nil {
		facility = *req.FacilityID
	}
	var userID interface{}
	if req.To.UserID != nil {
		userID = *req.To.UserID
	}

	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO notification_jobs (facility_id, channel, template_key, to_whatsapp, to_user_id, payload_json, status)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'pending')
RETURNING id`,
		facility, ch, nullStr(req.TemplateKey), nullStr(wa), userID, string(raw),
	).Scan(&id)
	if err != nil {
		return nil, err
	}

	job, err := s.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.process(ctx, job, req)
}

func (s *Service) process(ctx context.Context, job *Job, req SendRequest) (*Job, error) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE notification_jobs SET status='processing', updated_at=NOW() WHERE id=$1`, job.ID)

	var lastErr error
	providerRef := ""
	status := "sent"

	if job.Channel == "whatsapp" || job.Channel == "both" {
		facilityID := int64(0)
		if req.FacilityID != nil {
			facilityID = *req.FacilityID
		}
		// Fill WhatsApp from app_users when client only sent user_id.
		if strings.TrimSpace(req.To.WhatsApp) == "" && req.To.UserID != nil && *req.To.UserID > 0 {
			if phone := s.ResolvePhoneByUserID(ctx, *req.To.UserID); phone != "" {
				req.To.WhatsApp = phone
			}
		}
		waDigits := normalizePhone(req.To.WhatsApp)
		if waDigits == "" {
			// Channel both + user_id only → skip WhatsApp, still try push below.
			if job.Channel == "both" {
				providerRef = "wa_skip:no_phone"
			} else {
				lastErr = fmt.Errorf("whatsapp_required")
				status = "failed"
			}
		} else {
			credsRow, err := s.ResolveWhatsAppCreds(ctx, facilityID)
			if err != nil {
				lastErr = err
				status = "failed"
			} else if facilityID > 0 && !credsRow.WhatsAppEnabled {
				lastErr = fmt.Errorf("whatsapp_disabled_for_facility")
				status = "failed"
			} else {
				ref, err := s.WA.Send(ctx, req, WhatsAppSendCreds{
					AccessToken:   credsRow.ResolvedToken,
					PhoneNumberID: credsRow.ResolvedPhoneID,
				})
				if err != nil {
					lastErr = err
					status = "failed"
				} else {
					providerRef = ref
					if strings.HasPrefix(ref, "dry_run:") {
						status = "dry_run"
					}
				}
			}
		}
	}

	if job.Channel == "push" || job.Channel == "both" {
		{
			targetApp := pushTargetApp(req)
			waPhone := strings.TrimSpace(req.To.WhatsApp)
			// Facinect NotifyDispatch: phone → app_users.id when user_id omitted
			// OR when tokens missing for the given id (clients sometimes send
			// synthetic / stale user_ids from booking views).
			if waPhone != "" && targetApp != "strollx" {
				needPhoneResolve := req.To.UserID == nil || *req.To.UserID <= 0
				if !needPhoneResolve && len(req.To.DeviceTokens) == 0 {
					toks, _ := s.ResolveDeviceTokensForApp(ctx, *req.To.UserID, targetApp)
					needPhoneResolve = len(toks) == 0
				}
				if needPhoneResolve {
					if uid := s.ResolveUserIDByPhone(ctx, waPhone); uid > 0 {
						req.To.UserID = &uid
					}
				}
			}
			pushErr := error(nil)
			if len(req.To.DeviceTokens) == 0 && req.To.UserID != nil && *req.To.UserID > 0 {
				tokens, err := s.ResolveDeviceTokensForApp(ctx, *req.To.UserID, targetApp)
				if err != nil {
					pushErr = err
				} else if len(tokens) == 0 {
					pushErr = fmt.Errorf("no_device_tokens_for_user")
				} else {
					req.To.DeviceTokens = tokens
				}
			}
			if pushErr == nil && len(req.To.DeviceTokens) == 0 {
				pushErr = fmt.Errorf("device_token_required")
			}
			if pushErr == nil {
				ref, err := s.Push.Send(ctx, req)
				if err != nil {
					pushErr = err
				} else {
					if providerRef != "" {
						providerRef = providerRef + "," + ref
					} else {
						providerRef = ref
					}
					if strings.HasPrefix(ref, "dry_run:") && status == "sent" {
						status = "dry_run"
					}
					// Proximity inbox only. StrollX staff ids are not app_users.
					if req.To.UserID != nil && *req.To.UserID > 0 && targetApp != "strollx" {
						fid := int64(0)
						if req.FacilityID != nil {
							fid = *req.FacilityID
						}
						title, body := titleBodyFromData(req)
						_ = s.writeAppNotification(ctx, *req.To.UserID, fid, title, body, req.TemplateKey, map[string]interface{}{
							"source":   "notifications_send",
							"job_id":   job.ID,
							"channel":  job.Channel,
							"template": req.TemplateKey,
						})
					}
				}
			}
			if pushErr != nil {
				waSkipped := strings.Contains(providerRef, "wa_skip:")
				waDelivered := lastErr == nil && (status == "sent" || status == "dry_run")
				if job.Channel == "both" && waDelivered {
					// WhatsApp already went out. A push failure must not cancel it.
					if providerRef != "" {
						providerRef = providerRef + ",push_skip:" + pushErr.Error()
					} else {
						providerRef = "push_skip:" + pushErr.Error()
					}
				} else if job.Channel == "push" || waSkipped || job.Channel != "both" {
					lastErr = pushErr
					status = "failed"
				} else if lastErr != nil {
					// Both engines failed. Keep the first error and note push.
					lastErr = fmt.Errorf("%s; push:%s", lastErr.Error(), pushErr.Error())
					status = "failed"
				} else {
					lastErr = pushErr
					status = "failed"
				}
			} else if job.Channel == "both" && status == "failed" {
				// Push delivered after WhatsApp failed. Do not stop the notification.
				waNote := "wa_failed"
				if lastErr != nil && lastErr.Error() != "" {
					waNote = "wa_failed:" + lastErr.Error()
				}
				status = "sent"
				lastErr = nil
				if providerRef != "" {
					providerRef = providerRef + "," + waNote
				} else {
					providerRef = waNote
				}
			}
		}
	}

	errText := ""
	if lastErr != nil {
		errText = lastErr.Error()
	}
	if _, err := s.DB.ExecContext(ctx, `
UPDATE notification_jobs
SET status=$2, provider_ref=$3, error_text=$4, updated_at=NOW()
WHERE id=$1`, job.ID, status, nullStr(providerRef), nullStr(errText)); err != nil {
		// Still return in-memory outcome so clients don't see stale "processing".
		job.Status = status
		job.ProviderRef = providerRef
		job.ErrorText = errText
		return job, nil
	}

	_ = s.writeLog(ctx, job.ID, req, status)

	out, err := s.GetJob(ctx, job.ID)
	if err != nil || out == nil {
		job.Status = status
		job.ProviderRef = providerRef
		job.ErrorText = errText
		return job, nil
	}
	// Prefer final status we just wrote (avoid rare read-your-writes lag).
	if strings.TrimSpace(out.Status) == "" ||
		strings.EqualFold(out.Status, "processing") ||
		strings.EqualFold(out.Status, "pending") {
		out.Status = status
		out.ProviderRef = providerRef
		out.ErrorText = errText
	}
	return out, nil
}

// pushTargetApp picks which app's FCM tokens may receive this push.
// Staff booking alerts go to StrollX. Customer alerts stay on Proximity.
func pushTargetApp(req SendRequest) string {
	if strings.EqualFold(strings.TrimSpace(req.TemplateKey), "booking_staff_notify") {
		return "strollx"
	}
	if req.Data != nil {
		app, _ := req.Data["app"].(string)
		if strings.EqualFold(strings.TrimSpace(app), "strollx") {
			return "strollx"
		}
	}
	return "proximity"
}

func normalizePushApp(app string) string {
	if strings.EqualFold(strings.TrimSpace(app), "strollx") {
		return "strollx"
	}
	return "proximity"
}

func titleBodyFromData(req SendRequest) (string, string) {
	resolvedTmpl, _ := resolveWhatsAppTemplate(req.TemplateKey, "")
	if params := bodyParamStrings(req.Data); len(params) >= 3 &&
		resolvedTmpl == facilityDynamicTemplate {
		return params[0], FacilityDynamicMessage(params[0], params[1], params[2])
	}
	title := strings.TrimSpace(req.TemplateKey)
	if title == "" {
		title = "Facinect"
	}
	body := title
	if req.Data != nil {
		if v, ok := req.Data["title"].(string); ok && strings.TrimSpace(v) != "" {
			title = strings.TrimSpace(v)
		}
		if v, ok := req.Data["body"].(string); ok && strings.TrimSpace(v) != "" {
			body = strings.TrimSpace(v)
		} else if v, ok := req.Data["message"].(string); ok && strings.TrimSpace(v) != "" {
			body = strings.TrimSpace(v)
		}
	}
	return title, body
}

// ResolvePhoneByUserID returns WhatsApp/phone digits for an app user.
func (s *Service) ResolvePhoneByUserID(ctx context.Context, userID int64) string {
	if userID <= 0 {
		return ""
	}
	var wa, phone string
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(whatsapp_no,''), COALESCE(phone,'')
FROM app_users WHERE id=$1 AND status='active'`, userID).Scan(&wa, &phone)
	if err != nil {
		return ""
	}
	if p := normalizePhone(wa); p != "" {
		return p
	}
	return normalizePhone(phone)
}

// ResolveUserIDByPhone maps WhatsApp digits → app_users.id (Facinect users.userId).
// Matches last 10 digits so 7708054914 and 917708054914 resolve the same user.
func (s *Service) ResolveUserIDByPhone(ctx context.Context, phone string) int64 {
	p := digitsOnlyPhone(phone)
	if len(p) < 10 {
		return 0
	}
	last10 := p[len(p)-10:]
	var id int64
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM app_users
WHERE status='active'
  AND RIGHT(regexp_replace(COALESCE(whatsapp_no,''), '\D', '', 'g'), 10) = $1
LIMIT 1`, last10).Scan(&id)
	if err != nil {
		// Also try phone column when whatsapp_no is empty/mismatched.
		err = s.DB.QueryRowContext(ctx, `
SELECT id FROM app_users
WHERE status='active'
  AND RIGHT(regexp_replace(COALESCE(phone,''), '\D', '', 'g'), 10) = $1
LIMIT 1`, last10).Scan(&id)
		if err != nil {
			return 0
		}
	}
	return id
}

func (s *Service) writeLog(ctx context.Context, jobID int64, req SendRequest, status string) error {
	title := ""
	body := ""
	if req.Data != nil {
		if v, ok := req.Data["title"].(string); ok {
			title = v
		}
		if v, ok := req.Data["body"].(string); ok {
			body = v
		}
		if body == "" {
			if v, ok := req.Data["message"].(string); ok {
				body = v
			}
		}
	}
	if title == "" {
		title = strings.TrimSpace(req.TemplateKey)
	}
	meta, _ := json.Marshal(req.Data)
	var facility, userID interface{}
	if req.FacilityID != nil {
		facility = *req.FacilityID
	}
	if req.To.UserID != nil {
		userID = *req.To.UserID
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO notification_log (job_id, facility_id, user_id, channel, template_key, title, body, to_whatsapp, status, meta_json)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`,
		jobID, facility, userID, strings.ToLower(strings.TrimSpace(req.Channel)),
		nullStr(req.TemplateKey), nullStr(title), nullStr(body),
		nullStr(normalizePhone(req.To.WhatsApp)), status, string(meta),
	)
	return err
}

type LogItem struct {
	ID          int64           `json:"id"`
	JobID       *int64          `json:"job_id,omitempty"`
	FacilityID  *int64          `json:"facility_id,omitempty"`
	UserID      *int64          `json:"user_id,omitempty"`
	Channel     string          `json:"channel"`
	TemplateKey string          `json:"template_key,omitempty"`
	Title       string          `json:"title,omitempty"`
	Body        string          `json:"body,omitempty"`
	ToWhatsApp  string          `json:"to_whatsapp,omitempty"`
	Status      string          `json:"status"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	SeenAt      *time.Time      `json:"seen_at,omitempty"`
}

func (s *Service) History(ctx context.Context, facilityID *int64, limit int) ([]LogItem, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	q := `
SELECT id, job_id, facility_id, user_id, channel, COALESCE(template_key,''), COALESCE(title,''), COALESCE(body,''),
       COALESCE(to_whatsapp,''), status, meta_json, created_at, seen_at
FROM notification_log WHERE 1=1`
	args := []interface{}{}
	n := 1
	if facilityID != nil && *facilityID > 0 {
		q += fmt.Sprintf(` AND facility_id=$%d`, n)
		args = append(args, *facilityID)
		n++
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	return s.scanLog(ctx, q, args...)
}

func (s *Service) Inbox(ctx context.Context, userID *int64, email string, limit int) ([]LogItem, error) {
	if limit <= 0 {
		limit = 40
	}
	if limit > 200 {
		limit = 200
	}
	if userID == nil || *userID <= 0 {
		// email alone cannot resolve without identity join — return empty rather than fail
		_ = email
		return []LogItem{}, nil
	}
	s.ensureLogDeleted(ctx)
	q := `
SELECT id, job_id, facility_id, user_id, channel, COALESCE(template_key,''), COALESCE(title,''), COALESCE(body,''),
       COALESCE(to_whatsapp,''), status, meta_json, created_at, seen_at
FROM notification_log
WHERE user_id=$1 AND deleted_at IS NULL
ORDER BY created_at DESC LIMIT $2`
	return s.scanLog(ctx, q, *userID, limit)
}

// DeleteInbox hides one or all inbox rows for the logged-in StrollX user.
func (s *Service) DeleteInbox(ctx context.Context, userID int64, ids []int64, deleteAll bool) (int, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("unauthorized")
	}
	s.ensureLogDeleted(ctx)
	if deleteAll {
		res, err := s.DB.ExecContext(ctx, `
UPDATE notification_log SET deleted_at=NOW()
WHERE user_id=$1 AND deleted_at IS NULL`, userID)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}
	n := 0
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		res, err := s.DB.ExecContext(ctx, `
UPDATE notification_log SET deleted_at=NOW()
WHERE user_id=$1 AND id=$2 AND deleted_at IS NULL`, userID, id)
		if err != nil {
			return n, err
		}
		affected, _ := res.RowsAffected()
		n += int(affected)
	}
	return n, nil
}

func (s *Service) ensureLogDeleted(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
ALTER TABLE notification_log ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL`)
}

func (s *Service) scanLog(ctx context.Context, q string, args ...interface{}) ([]LogItem, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogItem{}
	for rows.Next() {
		var it LogItem
		var jobID, facility, userID sql.NullInt64
		var meta []byte
		var seen sql.NullTime
		if err := rows.Scan(
			&it.ID, &jobID, &facility, &userID, &it.Channel, &it.TemplateKey, &it.Title, &it.Body,
			&it.ToWhatsApp, &it.Status, &meta, &it.CreatedAt, &seen,
		); err != nil {
			return nil, err
		}
		if jobID.Valid {
			v := jobID.Int64
			it.JobID = &v
		}
		if facility.Valid {
			v := facility.Int64
			it.FacilityID = &v
		}
		if userID.Valid {
			v := userID.Int64
			it.UserID = &v
		}
		if seen.Valid {
			t := seen.Time
			it.SeenAt = &t
		}
		it.Meta = meta
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Service) GetJob(ctx context.Context, id int64) (*Job, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, channel, COALESCE(template_key,''), COALESCE(to_whatsapp,''), to_user_id,
       payload_json, status, COALESCE(provider_ref,''), COALESCE(error_text,''), created_at, updated_at
FROM notification_jobs WHERE id=$1`, id)
	var j Job
	var facility, userID sql.NullInt64
	var payload []byte
	if err := row.Scan(
		&j.ID, &facility, &j.Channel, &j.TemplateKey, &j.ToWhatsApp, &userID,
		&payload, &j.Status, &j.ProviderRef, &j.ErrorText, &j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if facility.Valid {
		v := facility.Int64
		j.FacilityID = &v
	}
	if userID.Valid {
		v := userID.Int64
		j.ToUserID = &v
	}
	j.Payload = payload
	return &j, nil
}

func (s *Service) RegisterDevice(ctx context.Context, userID int64, token, platform, app string) error {
	token = strings.TrimSpace(token)
	platform = strings.ToLower(strings.TrimSpace(platform))
	app = normalizePushApp(app)
	if userID <= 0 || token == "" {
		return fmt.Errorf("invalid_device")
	}
	if platform == "" {
		platform = "android"
	}
	if platform != "android" && platform != "ios" && platform != "web" {
		return fmt.Errorf("invalid_platform")
	}
	s.ensureDeviceTokenApp(ctx)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO device_tokens (user_id, token, platform, status, app)
VALUES ($1, $2, $3, 'active', $4)
ON CONFLICT (user_id, token) DO UPDATE
SET platform=EXCLUDED.platform, status='active', app=EXCLUDED.app, updated_at=NOW()`,
		userID, token, platform, app)
	if err != nil {
		return err
	}
	if app == "strollx" {
		// Staff tokens must not be delivered as Proximity customer pushes.
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_fcm_devices WHERE token=$1`, token)
		return nil
	}
	// Keep proximity app_fcm_devices in sync (same DB).
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_fcm_devices (user_id, token, platform, device_id, updated_at)
VALUES ($1, $2, $3, '', NOW())
ON CONFLICT (token) DO UPDATE
SET user_id=EXCLUDED.user_id, platform=EXCLUDED.platform, updated_at=NOW()`,
		userID, token, platform)
	return nil
}

func (s *Service) ensureDeviceTokenApp(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
ALTER TABLE device_tokens ADD COLUMN IF NOT EXISTS app VARCHAR(16) NOT NULL DEFAULT 'proximity'`)
}

// ResolveDeviceTokens loads Proximity tokens. StrollX staff tokens are excluded.
func (s *Service) ResolveDeviceTokens(ctx context.Context, userID int64) ([]string, error) {
	return s.ResolveDeviceTokensForApp(ctx, userID, "proximity")
}

// ResolveDeviceTokensForApp loads active FCM tokens for one app.
// Proximity also reads app_fcm_devices. StrollX reads device_tokens.app only.
func (s *Service) ResolveDeviceTokensForApp(ctx context.Context, userID int64, app string) ([]string, error) {
	if userID <= 0 {
		return nil, nil
	}
	app = normalizePushApp(app)
	seen := map[string]struct{}{}
	var out []string
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.HasPrefix(tok, "test-") || len(tok) < 40 {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT token FROM device_tokens
WHERE user_id=$1 AND status='active' AND token <> '' AND app=$2
ORDER BY updated_at DESC`, userID, app)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		add(t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if app != "proximity" {
		return out, nil
	}
	rows2, err := s.DB.QueryContext(ctx, `
SELECT d.token FROM app_fcm_devices d
WHERE d.user_id=$1 AND d.token <> ''
  AND NOT EXISTS (
    SELECT 1 FROM device_tokens t
    WHERE t.token = d.token AND t.app = 'strollx' AND t.status = 'active'
  )
ORDER BY d.updated_at DESC`, userID)
	if err != nil {
		// Table may not exist on older DBs — ignore.
		if !strings.Contains(err.Error(), "does not exist") {
			return out, err
		}
		return out, nil
	}
	defer rows2.Close()
	for rows2.Next() {
		var t string
		if err := rows2.Scan(&t); err != nil {
			return out, err
		}
		add(t)
	}
	return out, rows2.Err()
}

func normalizePhone(p string) string {
	return digitsOnlyPhone(p)
}

// digitsOnlyPhone keeps digits only; prefixes 91 for bare 10-digit Indian mobiles.
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

func nullStr(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
