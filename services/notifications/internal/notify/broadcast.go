package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type ServiceBroadcastRequest struct {
	Title                    string  `json:"title"`
	Body                     string  `json:"body"`
	Scope                    string  `json:"scope"`
	Audience                 string  `json:"audience"`
	FacilityIDs              []int64 `json:"facilityIds"`
	OrganisationIDs          []int64 `json:"organisationIds"`
	ServiceType              string  `json:"serviceType"`
	SenderFacilitiesUserID   int64   `json:"senderFacilitiesUserId"`
	SportID                  int64   `json:"sportId"`
	SenderChannel            string  `json:"senderChannel"`
}

type ServiceBroadcastResult struct {
	OK            bool    `json:"ok"`
	Message       string  `json:"message"`
	AppPush       int     `json:"appPush"`
	FcmPush       int     `json:"fcmPush"`
	NoAppUser     int     `json:"noAppUser"`
	NoFcmToken    int     `json:"noFcmToken"`
	AudienceSize  int     `json:"audienceSize"`
	FacilityIDs   []int64 `json:"facilityIds"`
	TargetUserIDs []int64 `json:"targetUserIds"`
	BroadcastID   int64   `json:"broadcastId"`
}

// ServiceBroadcast resolves facility end-users (and/or staff phones mapped to
// app_users), writes inbox rows, and sends FCM — matching StrollX service messages.
func (s *Service) ServiceBroadcast(ctx context.Context, req ServiceBroadcastRequest) (*ServiceBroadcastResult, error) {
	title := strings.TrimSpace(req.Title)
	body := strings.TrimSpace(req.Body)
	if title == "" || body == "" {
		return &ServiceBroadcastResult{OK: false, Message: "Title and body are required."}, nil
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "facilities"
	}
	audience := strings.ToLower(strings.TrimSpace(req.Audience))
	if audience == "" {
		audience = "facility_end_users"
	}
	serviceType := strings.TrimSpace(req.ServiceType)
	if serviceType == "" {
		serviceType = "announcement"
	}

	facilityIDs, err := s.resolveBroadcastFacilities(ctx, scope, req.FacilityIDs, req.OrganisationIDs)
	if err != nil {
		return nil, err
	}
	if len(facilityIDs) == 0 {
		return &ServiceBroadcastResult{OK: false, Message: "No facilities in scope."}, nil
	}

	userIDs, err := s.resolveAudienceUserIDs(ctx, audience, facilityIDs, req.SportID)
	if err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return &ServiceBroadcastResult{
			OK:          false,
			Message:     "No recipients found for this scope/audience.",
			FacilityIDs: facilityIDs,
		}, nil
	}

	primaryFacility := facilityIDs[0]
	meta := map[string]interface{}{
		"source":           "service_notify",
		"serviceType":      serviceType,
		"scope":            scope,
		"facilityIds":      facilityIDs,
		"organisationIds":  req.OrganisationIDs,
		"audience":         audience,
		"senderChannel":    req.SenderChannel,
		"type":             "service_notify",
	}

	appPush, fcmPush, noToken := 0, 0, 0
	targets := make([]int64, 0, len(userIDs))

	for _, uid := range userIDs {
		if err := s.writeAppNotification(ctx, uid, primaryFacility, title, body, serviceType, meta); err != nil {
			continue
		}
		appPush++
		targets = append(targets, uid)

		tokens, err := s.ResolveDeviceTokens(ctx, uid)
		if err != nil || len(tokens) == 0 {
			noToken++
			continue
		}
		_, err = s.Push.Send(ctx, SendRequest{
			Channel:     "push",
			FacilityID:  &primaryFacility,
			TemplateKey: serviceType,
			To:          SendTo{UserID: &uid, DeviceTokens: tokens},
			Data: map[string]interface{}{
				"title":    title,
				"body":     body,
				"type":     "service_notify",
				"scope":    scope,
				"audience": audience,
			},
		})
		if err != nil {
			continue
		}
		fcmPush++
	}

	broadcastID, _ := s.insertServiceBroadcastHistory(ctx, req, facilityIDs, appPush, fcmPush, noToken, len(userIDs))

	msg := fmt.Sprintf(
		"Broadcast to %d recipient(s) across %d facility(ies). appPush=%d fcmPush=%d",
		len(userIDs), len(facilityIDs), appPush, fcmPush,
	)
	return &ServiceBroadcastResult{
		OK:            appPush > 0 || fcmPush > 0,
		Message:       msg,
		AppPush:       appPush,
		FcmPush:       fcmPush,
		NoFcmToken:    noToken,
		AudienceSize:  len(userIDs),
		FacilityIDs:   facilityIDs,
		TargetUserIDs: targets,
		BroadcastID:   broadcastID,
	}, nil
}

func (s *Service) resolveBroadcastFacilities(ctx context.Context, scope string, facilityIDs, orgIDs []int64) ([]int64, error) {
	seen := map[int64]struct{}{}
	var out []int64
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	switch scope {
	case "all":
		rows, err := s.DB.QueryContext(ctx, `SELECT id FROM facilities ORDER BY id ASC LIMIT 500`)
		if err != nil {
			// app_facilities fallback
			rows2, err2 := s.DB.QueryContext(ctx, `SELECT id FROM app_facilities ORDER BY id ASC LIMIT 500`)
			if err2 != nil {
				return nil, err
			}
			defer rows2.Close()
			for rows2.Next() {
				var id int64
				if err := rows2.Scan(&id); err == nil {
					add(id)
				}
			}
			return out, nil
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				add(id)
			}
		}
	default:
		for _, id := range facilityIDs {
			add(id)
		}
	}

	if scope == "organisations" || len(orgIDs) > 0 {
		for _, oid := range orgIDs {
			if oid <= 0 {
				continue
			}
			rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM facilities
WHERE organisation_id=$1 OR organisationid=$1
LIMIT 200`, oid)
			if err != nil {
				// column may not exist — ignore
				continue
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err == nil {
					add(id)
				}
			}
			rows.Close()
		}
	}
	return out, nil
}

func (s *Service) ensureAudienceMap(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_fcm_audience_map (
  id             BIGSERIAL PRIMARY KEY,
  audience       VARCHAR(32) NOT NULL,
  role_id        BIGINT NOT NULL,
  users_user_id  BIGINT NOT NULL,
  phone_norm     VARCHAR(20),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (audience, role_id)
)`)
}

func newIDSet() (func(int64), *[]int64) {
	seen := map[int64]struct{}{}
	out := []int64{}
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return add, &out
}

// resolveAudienceUserIDs picks recipients by broadcast audience filter.
// Prefer app_fcm_audience_map (synced on FCM register); fall back to phone joins.
func (s *Service) resolveAudienceUserIDs(ctx context.Context, audience string, facilityIDs []int64, sportID int64) ([]int64, error) {
	s.ensureAudienceMap(ctx)
	switch audience {
	case "members", "member":
		return s.resolveMemberUserIDs(ctx, facilityIDs, sportID)
	case "students", "student":
		return s.resolveStudentUserIDs(ctx, facilityIDs, sportID)
	case "management", "staff", "facility_staff":
		return s.resolveManagementUserIDs(ctx, facilityIDs)
	default:
		// facility_end_users / end_users / users / all
		return s.resolveEndUserIDs(ctx, facilityIDs, sportID)
	}
}

func (s *Service) resolveUserIDsFromAudienceMap(ctx context.Context, audience string, roleIDs []int64) []int64 {
	if len(roleIDs) == 0 {
		return nil
	}
	add, out := newIDSet()
	// Chunk IN lists to keep query size bounded.
	const chunk = 200
	for i := 0; i < len(roleIDs); i += chunk {
		end := i + chunk
		if end > len(roleIDs) {
			end = len(roleIDs)
		}
		part := roleIDs[i:end]
		placeholders := make([]string, len(part))
		args := make([]interface{}, 0, len(part)+1)
		args = append(args, audience)
		for j, id := range part {
			placeholders[j] = fmt.Sprintf("$%d", j+2)
			args = append(args, id)
		}
		q := fmt.Sprintf(`
SELECT DISTINCT users_user_id FROM app_fcm_audience_map
WHERE audience=$1 AND role_id IN (%s) AND users_user_id>0
LIMIT 500`, strings.Join(placeholders, ","))
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				add(id)
			}
		}
		rows.Close()
	}
	return *out
}

func (s *Service) resolveMemberUserIDs(ctx context.Context, facilityIDs []int64, sportID int64) ([]int64, error) {
	add, out := newIDSet()
	roleIDs := []int64{}
	for _, fid := range facilityIDs {
		q := `SELECT DISTINCT member_id FROM memberships WHERE facility_id=$1 AND status='active' AND member_id>0`
		args := []interface{}{fid}
		if sportID > 0 {
			q += ` AND sport_id=$2`
			args = append(args, sportID)
		}
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var mid int64
			if rows.Scan(&mid) == nil && mid > 0 {
				roleIDs = append(roleIDs, mid)
			}
		}
		rows.Close()
	}
	for _, uid := range s.resolveUserIDsFromAudienceMap(ctx, "member", roleIDs) {
		add(uid)
	}
	// Phone fallback when map not yet synced for some members.
	if len(*out) == 0 || len(*out) < len(roleIDs) {
		for _, fid := range facilityIDs {
			q := `
SELECT DISTINCT u.id
FROM memberships ms
JOIN members m ON m.id = ms.member_id
JOIN app_users u ON RIGHT(regexp_replace(COALESCE(u.whatsapp_no,''), '\D', '', 'g'), 10)
  = RIGHT(regexp_replace(COALESCE(NULLIF(m.whatsapp,''), NULLIF(m.contact_phone,'')), '\D', '', 'g'), 10)
WHERE ms.facility_id=$1 AND ms.status='active' AND m.status='active'
  AND length(regexp_replace(COALESCE(NULLIF(m.whatsapp,''), NULLIF(m.contact_phone,'')), '\D', '', 'g')) >= 10`
			args := []interface{}{fid}
			if sportID > 0 {
				q += ` AND ms.sport_id=$2`
				args = append(args, sportID)
			}
			rows, err := s.DB.QueryContext(ctx, q, args...)
			if err != nil {
				continue
			}
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					add(id)
				}
			}
			rows.Close()
		}
	}
	return *out, nil
}

func (s *Service) resolveStudentUserIDs(ctx context.Context, facilityIDs []int64, sportID int64) ([]int64, error) {
	add, out := newIDSet()
	roleIDs := []int64{}
	for _, fid := range facilityIDs {
		q := `SELECT DISTINCT student_id FROM student_enrollments WHERE facility_id=$1 AND status='active' AND student_id>0`
		args := []interface{}{fid}
		if sportID > 0 {
			q += ` AND sport_id=$2`
			args = append(args, sportID)
		}
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var sid int64
			if rows.Scan(&sid) == nil && sid > 0 {
				roleIDs = append(roleIDs, sid)
			}
		}
		rows.Close()
	}
	for _, uid := range s.resolveUserIDsFromAudienceMap(ctx, "student", roleIDs) {
		add(uid)
	}
	if len(*out) == 0 || len(*out) < len(roleIDs) {
		for _, fid := range facilityIDs {
			q := `
SELECT DISTINCT u.id
FROM student_enrollments se
JOIN students st ON st.id = se.student_id
JOIN app_users u ON RIGHT(regexp_replace(COALESCE(u.whatsapp_no,''), '\D', '', 'g'), 10)
  = RIGHT(regexp_replace(COALESCE(NULLIF(st.whatsapp,''), NULLIF(st.contact_phone,'')), '\D', '', 'g'), 10)
WHERE se.facility_id=$1 AND se.status='active' AND st.status='active'
  AND length(regexp_replace(COALESCE(NULLIF(st.whatsapp,''), NULLIF(st.contact_phone,'')), '\D', '', 'g')) >= 10`
			args := []interface{}{fid}
			if sportID > 0 {
				q += ` AND se.sport_id=$2`
				args = append(args, sportID)
			}
			rows, err := s.DB.QueryContext(ctx, q, args...)
			if err != nil {
				continue
			}
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					add(id)
				}
			}
			rows.Close()
		}
	}
	return *out, nil
}

func (s *Service) resolveManagementUserIDs(ctx context.Context, facilityIDs []int64) ([]int64, error) {
	add, out := newIDSet()
	roleIDs := []int64{}
	for _, fid := range facilityIDs {
		rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT user_id FROM user_facility_memberships
WHERE facility_id=$1 AND status='active' AND user_id>0`, fid)
		if err != nil {
			continue
		}
		for rows.Next() {
			var uid int64
			if rows.Scan(&uid) == nil && uid > 0 {
				roleIDs = append(roleIDs, uid)
			}
		}
		rows.Close()
	}
	for _, uid := range s.resolveUserIDsFromAudienceMap(ctx, "management", roleIDs) {
		add(uid)
	}
	return *out, nil
}

func (s *Service) resolveEndUserIDs(ctx context.Context, facilityIDs []int64, sportID int64) ([]int64, error) {
	add, out := newIDSet()

	for _, fid := range facilityIDs {
		// 1) App users who accessed this facility
		rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM app_users
WHERE status='active' AND $1 = ANY(accessed_facility_ids)`, fid)
		if err == nil {
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err == nil {
					add(id)
				}
			}
			rows.Close()
		}

		// 2) Booking users
		rows2, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT user_id FROM app_bookings WHERE facility_id=$1 AND user_id>0`, fid)
		if err == nil {
			for rows2.Next() {
				var id int64
				if err := rows2.Scan(&id); err == nil {
					add(id)
				}
			}
			rows2.Close()
		}

		// 3) Members via audience_map + phone fallback
		memberIDs, _ := s.resolveMemberUserIDs(ctx, []int64{fid}, sportID)
		for _, id := range memberIDs {
			add(id)
		}

		// 4) Students via audience_map + phone fallback
		studentIDs, _ := s.resolveStudentUserIDs(ctx, []int64{fid}, sportID)
		for _, id := range studentIDs {
			add(id)
		}
	}

	// 5) Fallback: registered proximity users with an active FCM token.
	// Needed when accessed_facility_ids has not been populated yet (local/dev).
	if len(*out) == 0 {
		rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT u.id
FROM app_users u
WHERE u.status='active'
  AND (
    EXISTS (SELECT 1 FROM app_fcm_devices d WHERE d.user_id=u.id AND d.token<>'' AND d.token NOT LIKE 'test-%' AND length(d.token)>20)
    OR EXISTS (SELECT 1 FROM device_tokens t WHERE t.user_id=u.id AND t.status='active' AND t.token<>'' AND t.token NOT LIKE 'test-%' AND length(t.token)>20)
  )
ORDER BY u.id
LIMIT 200`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err == nil {
					add(id)
				}
			}
		}
	}
	return *out, nil
}

func (s *Service) writeAppNotification(ctx context.Context, userID, facilityID int64, title, body, ntype string, meta map[string]interface{}) error {
	raw, _ := json.Marshal(meta)
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_notifications (user_id, facility_id, ntype, title, body, source, meta_json)
VALUES ($1,$2,$3,$4,$5,'service',$6::jsonb)`,
		userID, nullInt64(facilityID), ntype, title, body, string(raw))
	return err
}

func (s *Service) insertServiceBroadcastHistory(
	ctx context.Context,
	req ServiceBroadcastRequest,
	facilityIDs []int64,
	appPush, fcmPush, noToken, audienceSize int,
) (int64, error) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_service_broadcasts (
  id BIGSERIAL PRIMARY KEY,
  title VARCHAR(255) NOT NULL,
  body TEXT NOT NULL,
  service_type VARCHAR(48) NOT NULL DEFAULT 'announcement',
  scope VARCHAR(32) NOT NULL DEFAULT 'facilities',
  facility_ids JSONB NULL,
  organisation_ids JSONB NULL,
  audience VARCHAR(32) NOT NULL DEFAULT 'facility_end_users',
  sender_facilities_user_id BIGINT NULL,
  app_push INT NOT NULL DEFAULT 0,
  fcm_push INT NOT NULL DEFAULT 0,
  no_app_user INT NOT NULL DEFAULT 0,
  no_fcm_token INT NOT NULL DEFAULT 0,
  audience_size INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	fids, _ := json.Marshal(facilityIDs)
	oids, _ := json.Marshal(req.OrganisationIDs)
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO app_service_broadcasts
 (title, body, service_type, scope, facility_ids, organisation_ids, audience,
  sender_facilities_user_id, app_push, fcm_push, no_fcm_token, audience_size)
VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12)
RETURNING id`,
		req.Title, req.Body,
		nullStr(req.ServiceType), nullStr(req.Scope),
		string(fids), string(oids), nullStr(req.Audience),
		nullInt64(req.SenderFacilitiesUserID),
		appPush, fcmPush, noToken, audienceSize,
	).Scan(&id)
	return id, err
}

func nullInt64(v int64) interface{} {
	if v <= 0 {
		return nil
	}
	return v
}

type ServiceBroadcastHistoryItem struct {
	ID           int64           `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	ServiceType  string          `json:"service_type"`
	Scope        string          `json:"scope"`
	FacilityIDs  json.RawMessage `json:"facility_ids"`
	Audience     string          `json:"audience"`
	AppPush      int             `json:"app_push"`
	FcmPush      int             `json:"fcm_push"`
	NoFcmToken   int             `json:"no_fcm_token"`
	AudienceSize int             `json:"audience_size"`
	CreatedAt    string          `json:"created_at"`
}

func (s *Service) ListServiceBroadcasts(ctx context.Context, limit int) ([]ServiceBroadcastHistoryItem, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_service_broadcasts (
  id BIGSERIAL PRIMARY KEY,
  title VARCHAR(255) NOT NULL,
  body TEXT NOT NULL,
  service_type VARCHAR(48) NOT NULL DEFAULT 'announcement',
  scope VARCHAR(32) NOT NULL DEFAULT 'facilities',
  facility_ids JSONB NULL,
  organisation_ids JSONB NULL,
  audience VARCHAR(32) NOT NULL DEFAULT 'facility_end_users',
  sender_facilities_user_id BIGINT NULL,
  app_push INT NOT NULL DEFAULT 0,
  fcm_push INT NOT NULL DEFAULT 0,
  no_app_user INT NOT NULL DEFAULT 0,
  no_fcm_token INT NOT NULL DEFAULT 0,
  audience_size INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, title, body, COALESCE(service_type,''), COALESCE(scope,''), COALESCE(facility_ids,'[]'::jsonb),
       COALESCE(audience,''), app_push, fcm_push, no_fcm_token, audience_size, created_at::text
FROM app_service_broadcasts
ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceBroadcastHistoryItem{}
	for rows.Next() {
		var it ServiceBroadcastHistoryItem
		if err := rows.Scan(
			&it.ID, &it.Title, &it.Body, &it.ServiceType, &it.Scope, &it.FacilityIDs,
			&it.Audience, &it.AppPush, &it.FcmPush, &it.NoFcmToken, &it.AudienceSize, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
