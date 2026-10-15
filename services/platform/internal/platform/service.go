package platform

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/facinect/platform/internal/config"
)

var (
	ErrDuplicateOnboarding = errors.New("duplicate_onboarding")
	nonDigitRE             = regexp.MustCompile(`\D+`)
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

// --- Onboarding ---

type OnboardingStatusIn struct {
	Email          string `json:"email"`
	WhatsappNumber string `json:"whatsappNumber"`
}

func (s *Service) OnboardingStatus(ctx context.Context, in OnboardingStatusIn) map[string]interface{} {
	none := map[string]interface{}{"status": "none", "isActive": 0}
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"email":           strings.TrimSpace(strings.ToLower(in.Email)),
			"whatsappNumber":  strings.TrimSpace(in.WhatsappNumber),
			"canContinue":     true,
			"paths": map[string]interface{}{
				"facility":    none,
				"tournaments": none,
				"referee":     none,
			},
			"drafts": []interface{}{},
		},
	}
}

type DraftIn struct {
	Email          string          `json:"email"`
	WhatsappNumber string          `json:"whatsappNumber"`
	Draft          json.RawMessage `json:"draft"`
}

func (s *Service) GetDraft(ctx context.Context, email string) (map[string]interface{}, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return map[string]interface{}{"success": true, "data": nil}, nil
	}
	var draft []byte
	var wa string
	err := s.DB.QueryRowContext(ctx, `
SELECT draft, COALESCE(whatsapp_number,'') FROM platform_onboarding_drafts WHERE email=$1`, email).
		Scan(&draft, &wa)
	if err == sql.ErrNoRows {
		return map[string]interface{}{"success": true, "data": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw interface{}
	_ = json.Unmarshal(draft, &raw)
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"email":          email,
			"whatsappNumber": wa,
			"draft":          raw,
		},
	}, nil
}

func (s *Service) SaveDraft(ctx context.Context, in DraftIn) (map[string]interface{}, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" {
		return nil, fmt.Errorf("email_required")
	}
	draft := in.Draft
	if len(draft) == 0 {
		draft = []byte("{}")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO platform_onboarding_drafts (email, whatsapp_number, draft, updated_at)
VALUES ($1, NULLIF($2,''), $3::jsonb, NOW())
ON CONFLICT (email) DO UPDATE SET
  whatsapp_number=EXCLUDED.whatsapp_number,
  draft=EXCLUDED.draft,
  updated_at=NOW()`,
		email, strings.TrimSpace(in.WhatsappNumber), string(draft))
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"success": true}, nil
}

type SubmitIn struct {
	Email          string          `json:"email"`
	WhatsappNumber string          `json:"whatsappNumber"`
	Payload        json.RawMessage `json:"payload"`
}

func normalizeEmail(v string) string {
	return strings.TrimSpace(strings.ToLower(v))
}

func normalizeWhatsApp(v string) string {
	return nonDigitRE.ReplaceAllString(strings.TrimSpace(v), "")
}

func normalizeMapsURL(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return strings.TrimRight(v, "/")
	}
	// Host + path only (ignore query noise like utm_*).
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	return u.Host + path
}

func payloadString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok && raw != nil {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func (s *Service) findDuplicateOnboarding(
	ctx context.Context,
	kind, email, wa, mapsURL string,
) (int64, string, error) {
	// Active pipeline only — rejected/closed may re-apply.
	const active = `status IN ('submitted','approved','pending','active')`
	if email != "" {
		var id int64
		err := s.DB.QueryRowContext(ctx, `
SELECT id FROM platform_onboarding_requests
WHERE kind=$1 AND `+active+` AND lower(trim(COALESCE(email,'')))=$2
ORDER BY id DESC LIMIT 1`, kind, email).Scan(&id)
		if err == nil {
			return id, "email", nil
		}
		if err != sql.ErrNoRows {
			return 0, "", err
		}
	}
	if wa != "" {
		var id int64
		err := s.DB.QueryRowContext(ctx, `
SELECT id FROM platform_onboarding_requests
WHERE kind=$1 AND `+active+`
  AND regexp_replace(
        COALESCE(NULLIF(whatsapp_number,''), payload->>'whatsappNumber', payload->>'phone', ''),
        '\D', '', 'g'
      ) = $2
ORDER BY id DESC LIMIT 1`, kind, wa).Scan(&id)
		if err == nil {
			return id, "whatsapp", nil
		}
		if err != sql.ErrNoRows {
			return 0, "", err
		}
	}
	if kind == "facility" && mapsURL != "" {
		var id int64
		// Normalize stored URL the same way as normalizeMapsURL (host+path, no query).
		err := s.DB.QueryRowContext(ctx, `
SELECT id FROM platform_onboarding_requests
WHERE kind=$1 AND `+active+`
  AND lower(trim(trailing '/' from split_part(
        replace(replace(
          COALESCE(
            NULLIF(trim(payload->>'googleMapUrl'), ''),
            NULLIF(trim(payload->>'google_map_url'), ''),
            NULLIF(trim(payload->>'locationUrl'), ''),
            ''
          ),
          'https://', ''), 'http://', ''),
        '?', 1
      ))) = $2
ORDER BY id DESC LIMIT 1`, kind, mapsURL).Scan(&id)
		if err == nil {
			return id, "googleMapUrl", nil
		}
		if err != sql.ErrNoRows {
			return 0, "", err
		}
	}
	return 0, "", nil
}

func (s *Service) SubmitOnboarding(ctx context.Context, kind string, in SubmitIn) (map[string]interface{}, error) {
	kind = strings.TrimSpace(strings.ToLower(kind))
	switch kind {
	case "facility", "tournament", "referee":
	default:
		return nil, fmt.Errorf("invalid_kind")
	}
	payload := in.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var payloadMap map[string]interface{}
	_ = json.Unmarshal(payload, &payloadMap)
	if payloadMap == nil {
		payloadMap = map[string]interface{}{}
	}

	email := normalizeEmail(in.Email)
	if email == "" {
		email = normalizeEmail(payloadString(payloadMap, "email"))
	}
	wa := normalizeWhatsApp(in.WhatsappNumber)
	if wa == "" {
		wa = normalizeWhatsApp(payloadString(payloadMap, "whatsappNumber", "phone"))
	}
	mapsURL := ""
	if kind == "facility" {
		mapsURL = normalizeMapsURL(payloadString(payloadMap, "googleMapUrl", "google_map_url", "locationUrl"))
	}

	if dupID, field, err := s.findDuplicateOnboarding(ctx, kind, email, wa, mapsURL); err != nil {
		return nil, err
	} else if dupID > 0 {
		return map[string]interface{}{
			"success": false,
			"error":   ErrDuplicateOnboarding.Error(),
			"message": "Already submitted — wait for approval.",
			"data": map[string]interface{}{
				"id":           dupID,
				"kind":         kind,
				"matched_by":   field,
				"existing_id":  dupID,
			},
		}, ErrDuplicateOnboarding
	}

	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO platform_onboarding_requests (kind, email, whatsapp_number, payload, status)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4::jsonb, 'submitted')
RETURNING id`,
		kind,
		email,
		wa,
		string(payload),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"success": true,
		"data":    map[string]interface{}{"id": id, "kind": kind},
	}, nil
}

func (s *Service) SportsCatalog() map[string]interface{} {
	sports := []map[string]interface{}{
		{"id": 1, "name": "Badminton", "slug": "badminton"},
		{"id": 2, "name": "Tennis", "slug": "tennis"},
		{"id": 3, "name": "Table Tennis", "slug": "table-tennis"},
		{"id": 4, "name": "Squash", "slug": "squash"},
		{"id": 5, "name": "Football", "slug": "football"},
		{"id": 6, "name": "Cricket", "slug": "cricket"},
		{"id": 7, "name": "Basketball", "slug": "basketball"},
		{"id": 8, "name": "Swimming", "slug": "swimming"},
	}
	return map[string]interface{}{"success": true, "data": sports}
}

// --- News ---

func (s *Service) ListNews(ctx context.Context, audience string, facilityID int64, facilityIDs []int64, limit int) (map[string]interface{}, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	aud := strings.TrimSpace(audience)
	if aud == "end_users" {
		aud = "end_users"
	}
	ids := append([]int64{}, facilityIDs...)
	if facilityID > 0 {
		ids = append(ids, facilityID)
	}
	seen := map[int64]struct{}{}
	uniq := []int64{}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}

	q := `
SELECT id, title, COALESCE(body,''), audience, facility_id, published_at::text, created_at::text
FROM platform_news WHERE 1=1`
	args := []interface{}{}
	n := 1
	if aud != "" {
		q += fmt.Sprintf(` AND (audience=$%d OR audience='all' OR audience='end_users')`, n)
		args = append(args, aud)
		n++
	}
	if len(uniq) > 0 {
		q += fmt.Sprintf(` AND (facility_id IS NULL OR facility_id = ANY($%d::bigint[]))`, n)
		args = append(args, uniq)
		n++
	}
	q += fmt.Sprintf(` ORDER BY published_at DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var title, body, a, published, created string
		var fid sql.NullInt64
		if err := rows.Scan(&id, &title, &body, &a, &fid, &published, &created); err != nil {
			return nil, err
		}
		item := map[string]interface{}{
			"id": id, "title": title, "body": body, "audience": a,
			"published_at": published, "createdAt": created, "created_at": created,
			"imageUrl": "", "expiresAt": "",
		}
		if fid.Valid {
			item["facilityId"] = fid.Int64
		}
		items = append(items, item)
	}
	return map[string]interface{}{
		"success": true,
		"message": "OK",
		"data":    map[string]interface{}{"items": items, "count": len(items)},
	}, rows.Err()
}

func (s *Service) SaveNews(ctx context.Context, newsID int64, userKey string) (map[string]interface{}, error) {
	if newsID <= 0 {
		return nil, fmt.Errorf("invalid_news_id")
	}
	if userKey == "" {
		userKey = "anonymous"
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO platform_news_inbox (news_id, user_key) VALUES ($1,$2)`, newsID, userKey)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"success": true}, nil
}

func (s *Service) NewsInbox(ctx context.Context, userKey string) (map[string]interface{}, error) {
	if userKey == "" {
		userKey = "anonymous"
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT n.id, n.title, COALESCE(n.body,''), n.audience, i.saved_at::text
FROM platform_news_inbox i
JOIN platform_news n ON n.id = i.news_id
WHERE i.user_key=$1
ORDER BY i.saved_at DESC
LIMIT 50`, userKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var title, body, aud, saved string
		if err := rows.Scan(&id, &title, &body, &aud, &saved); err != nil {
			return nil, err
		}
		items = append(items, map[string]interface{}{
			"id": id, "title": title, "body": body, "audience": aud, "saved_at": saved,
		})
	}
	return map[string]interface{}{"success": true, "data": items}, rows.Err()
}

// --- Organisation ---

func (s *Service) GetOrgProfile(ctx context.Context, email string) (map[string]interface{}, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return map[string]interface{}{"success": true, "data": nil}, nil
	}
	var name string
	var profile []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(name,''), profile FROM platform_organisation_profiles WHERE email=$1`, email).
		Scan(&name, &profile)
	if err == sql.ErrNoRows {
		return map[string]interface{}{"success": true, "data": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw interface{}
	_ = json.Unmarshal(profile, &raw)
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"email": email, "name": name, "profile": raw,
		},
	}, nil
}

type OrgProfileIn struct {
	Email   string          `json:"email"`
	Name    string          `json:"name"`
	Profile json.RawMessage `json:"profile"`
}

func (s *Service) SaveOrgProfile(ctx context.Context, in OrgProfileIn) (map[string]interface{}, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" {
		return nil, fmt.Errorf("email_required")
	}
	profile := in.Profile
	if len(profile) == 0 {
		profile = []byte("{}")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO platform_organisation_profiles (email, name, profile, updated_at)
VALUES ($1, NULLIF($2,''), $3::jsonb, NOW())
ON CONFLICT (email) DO UPDATE SET
  name=EXCLUDED.name, profile=EXCLUDED.profile, updated_at=NOW()`,
		email, strings.TrimSpace(in.Name), string(profile))
	if err != nil {
		return nil, err
	}
	return s.GetOrgProfile(ctx, email)
}

// --- Controls ---

func (s *Service) ControlsSync(ctx context.Context, facilityID int64, since string, timeoutSec int) (map[string]interface{}, error) {
	if timeoutSec > 0 {
		if timeoutSec > 25 {
			timeoutSec = 25
		}
		deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				break
			case <-time.After(400 * time.Millisecond):
			}
			break // stub: no long-poll change detection yet
		}
	}
	version := int64(1)
	pages := []interface{}{}
	if facilityID > 0 {
		var ver int64
		var pagesRaw []byte
		err := s.DB.QueryRowContext(ctx, `
SELECT version, pages_enabled FROM platform_controls_version WHERE facility_id=$1`, facilityID).
			Scan(&ver, &pagesRaw)
		if err == nil {
			version = ver
			_ = json.Unmarshal(pagesRaw, &pages)
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"version":       version,
			"changed":       false,
			"pages_enabled": pages,
		},
	}, nil
}

// --- Privacy ---

type PrivacyIn struct {
	Action         string          `json:"action"`
	FacilityID     int64           `json:"facilityId"`
	UserID         int64           `json:"userId"`
	Email          string          `json:"email"`
	WhatsappNo     string          `json:"whatsappNo"`
	Text           string          `json:"text"`
	Version        string          `json:"version"`
	PolicyType     string          `json:"policyType"`
	PolicyVersion  string          `json:"policyVersion"`
	Source         string          `json:"source"`
	Payload        json.RawMessage `json:"payload"`
}

const platformUsersVersion = "1.0"

func privacyIdentityKey(in PrivacyIn) string {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email != "" {
		return email
	}
	if in.UserID > 0 {
		return fmt.Sprintf("uid:%d", in.UserID)
	}
	wa := strings.TrimSpace(in.WhatsappNo)
	if wa != "" {
		return "wa:" + wa
	}
	return ""
}

func (s *Service) privacyHasAccept(ctx context.Context, key, policyType string, facilityID int64, version string) bool {
	if key == "" {
		return false
	}
	var accepted bool
	err := s.DB.QueryRowContext(ctx, `
SELECT accepted FROM platform_privacy
WHERE email=$1 AND action='accept'
  AND COALESCE(payload->>'policyType','platform_users') = $2
  AND COALESCE(facility_id,0) = $3
  AND ($4='' OR COALESCE(payload->>'version', payload->>'policyVersion', '1.0') = $4)
  AND accepted = TRUE
ORDER BY updated_at DESC LIMIT 1`, key, policyType, facilityID, version).Scan(&accepted)
	return err == nil && accepted
}

func (s *Service) facilityPrivacyMeta(ctx context.Context, facilityID int64) (text, version, name string, hasPolicy, askEnabled bool) {
	version = "1.0"
	askEnabled = true
	var p []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT payload FROM platform_privacy
WHERE facility_id=$1 AND action='save_facility'
ORDER BY updated_at DESC LIMIT 1`, facilityID).Scan(&p)
	if err == nil {
		var raw map[string]interface{}
		_ = json.Unmarshal(p, &raw)
		if raw != nil {
			if t, ok := raw["text"].(string); ok {
				text = t
			}
			if v, ok := raw["version"].(string); ok && strings.TrimSpace(v) != "" {
				version = strings.TrimSpace(v)
			}
			if a, ok := raw["askEnabled"].(bool); ok {
				askEnabled = a
			}
		}
	}
	hasPolicy = askEnabled && strings.TrimSpace(text) != ""
	_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, facilityID).Scan(&name)
	if name == "" {
		_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, facilityID).Scan(&name)
	}
	return text, version, name, hasPolicy, askEnabled
}

func (s *Service) Privacy(ctx context.Context, in PrivacyIn) (map[string]interface{}, error) {
	action := strings.TrimSpace(strings.ToLower(in.Action))
	payload := in.Payload
	if len(payload) == 0 {
		blob, _ := json.Marshal(map[string]interface{}{
			"text":          in.Text,
			"version":       firstNonEmptyStr(in.PolicyVersion, in.Version, platformUsersVersion),
			"policyVersion": firstNonEmptyStr(in.PolicyVersion, in.Version, platformUsersVersion),
			"policyType":    firstNonEmptyStr(in.PolicyType, "platform_users"),
			"whatsappNo":    in.WhatsappNo,
			"userId":        in.UserID,
			"source":        firstNonEmptyStr(in.Source, "app"),
		})
		payload = blob
	}
	key := privacyIdentityKey(in)
	switch action {
	case "get_facility":
		text, version, name, hasPolicy, askEnabled := s.facilityPrivacyMeta(ctx, in.FacilityID)
		return map[string]interface{}{
			"success": true,
			"message": "Facility privacy.",
			"data": map[string]interface{}{
				"facilityId":   in.FacilityID,
				"facilityName": name,
				"hasPolicy":    hasPolicy,
				"askEnabled":   askEnabled,
				"text":         text,
				"version":      version,
				"url":          fmt.Sprintf("/legal/privacy_facility.php?facilityId=%d", in.FacilityID),
				"source":       "gateway",
			},
		}, nil
	case "check":
		if key == "" {
			return map[string]interface{}{
				"success": false,
				"message": "userId or whatsappNo is required.",
			}, nil
		}
		platAccepted := s.privacyHasAccept(ctx, key, "platform_users", 0, platformUsersVersion)
		platform := map[string]interface{}{
			"accepted":        platAccepted,
			"needsAccept":     !platAccepted,
			"policyType":      "platform_users",
			"requiredVersion": platformUsersVersion,
		}
		askEnabled := true
		facilityHas := false
		var facility map[string]interface{}
		if in.FacilityID > 0 {
			_, facVer, facName, has, ask := s.facilityPrivacyMeta(ctx, in.FacilityID)
			askEnabled = ask
			facilityHas = has
			if !askEnabled {
				return map[string]interface{}{
					"success": true,
					"message": "Privacy ask disabled for facility.",
					"data": map[string]interface{}{
						"platform": map[string]interface{}{
							"accepted": true, "needsAccept": false,
							"policyType": "platform_users", "requiredVersion": platformUsersVersion,
						},
						"facility":          nil,
						"onboarding":        nil,
						"facilityHasPolicy": false,
						"askEnabled":        false,
						"needsPlatform":     false,
						"needsFacility":     false,
					},
				}, nil
			}
			if facilityHas {
				facAccepted := s.privacyHasAccept(ctx, key, "facility", in.FacilityID, facVer)
				facility = map[string]interface{}{
					"accepted":        facAccepted,
					"needsAccept":     !facAccepted,
					"policyType":      "facility",
					"requiredVersion": facVer,
					"facilityId":      in.FacilityID,
					"facilityName":    facName,
				}
			}
		}
		needsFacility := facility != nil && facility["needsAccept"] == true
		return map[string]interface{}{
			"success": true,
			"message": "Privacy acceptance status.",
			"data": map[string]interface{}{
				"platform":          platform,
				"facility":          facility,
				"onboarding":        nil,
				"facilityHasPolicy": facilityHas,
				"askEnabled":        askEnabled,
				"needsPlatform":     !platAccepted,
				"needsFacility":     needsFacility,
			},
		}, nil
	case "save_facility":
		blob, _ := json.Marshal(map[string]interface{}{
			"text":       in.Text,
			"version":    firstNonEmptyStr(in.Version, platformUsersVersion),
			"askEnabled": true,
		})
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO platform_privacy (facility_id, email, action, payload, accepted, updated_at)
VALUES ($1, NULLIF($2,''), 'save_facility', $3::jsonb, FALSE, NOW())`,
			in.FacilityID, key, string(blob))
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"success": true,
			"message": "Facility privacy policy saved.",
			"data": map[string]interface{}{
				"facilityId": in.FacilityID,
				"text":       in.Text,
				"version":    firstNonEmptyStr(in.Version, platformUsersVersion),
			},
		}, nil
	case "accept":
		if key == "" {
			return map[string]interface{}{"success": false, "message": "userId or whatsappNo is required."}, nil
		}
		ptype := strings.ToLower(strings.TrimSpace(in.PolicyType))
		if ptype == "users" || ptype == "" {
			ptype = "platform_users"
		}
		ver := firstNonEmptyStr(in.PolicyVersion, in.Version, platformUsersVersion)
		fid := int64(0)
		if ptype == "facility" {
			fid = in.FacilityID
		}
		blob, _ := json.Marshal(map[string]interface{}{
			"policyType":    ptype,
			"version":       ver,
			"policyVersion": ver,
			"userId":        in.UserID,
			"whatsappNo":    in.WhatsappNo,
			"source":        firstNonEmptyStr(in.Source, "app"),
		})
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO platform_privacy (facility_id, email, action, payload, accepted, updated_at)
VALUES (NULLIF($1,0), $2, 'accept', $3::jsonb, TRUE, NOW())`,
			fid, key, string(blob))
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"success": true,
			"message": "Privacy accepted.",
			"data": map[string]interface{}{
				"accepted": true, "policyType": ptype, "version": ver, "facilityId": fid,
			},
		}, nil
	default:
		return nil, fmt.Errorf("invalid_action")
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

// --- Billing ---

func entitlementPayload() map[string]interface{} {
	return map[string]interface{}{
		"standard_active": true,
		"needs_plan":      false,
		"must_pay":        false,
		"show_claim_card": false,
		"paid_active":     true,
		"source":          "gateway",
	}
}

func planPriceINR(code string) float64 {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "pro":
		return 999
	case "addon_enquiry":
		return 299
	case "addon_offers":
		return 299
	case "tournament_credit":
		return 499
	default:
		return 0
	}
}

func (s *Service) BillingEntitlement(facilityID int64) map[string]interface{} {
	data := entitlementPayload()
	if facilityID > 0 {
		var plan string
		var std, paid bool
		var source string
		err := s.DB.QueryRow(`
SELECT plan_code, standard_active, paid_active, source
FROM platform_billing_entitlements WHERE facility_id=$1`, facilityID).
			Scan(&plan, &std, &paid, &source)
		if err == nil {
			data["plan_code"] = plan
			data["standard_active"] = std
			data["paid_active"] = paid
			data["source"] = source
			data["needs_plan"] = !std
			data["must_pay"] = !paid && !std
		}
	}
	return map[string]interface{}{"success": true, "data": data}
}

func (s *Service) BillingCatalog() map[string]interface{} {
	plans := []map[string]interface{}{
		{"code": "standard", "name": "Standard", "price": 0, "currency": "INR", "interval": "month"},
		{"code": "pro", "name": "Pro", "price": 999, "currency": "INR", "interval": "month"},
		{"code": "addon_enquiry", "name": "Enquiry add-on", "price": 299, "currency": "INR", "interval": "month"},
		{"code": "addon_offers", "name": "Offers add-on", "price": 299, "currency": "INR", "interval": "month"},
		{"code": "tournament_credit", "name": "Tournament credit", "price": 499, "currency": "INR", "interval": "one_time"},
		{"code": "enterprise", "name": "Enterprise", "price": 0, "currency": "INR", "interval": "custom"},
	}
	return map[string]interface{}{
		"success": true,
		"data":    map[string]interface{}{"plans": plans, "source": "gateway"},
	}
}

func (s *Service) ClaimTrial(facilityID int64) map[string]interface{} {
	if facilityID > 0 {
		_, _ = s.DB.Exec(`
INSERT INTO platform_billing_entitlements (facility_id, plan_code, standard_active, paid_active, source, updated_at)
VALUES ($1, 'standard', true, true, 'trial', NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  plan_code='standard', standard_active=true, paid_active=true, source='trial', updated_at=NOW()`,
			facilityID)
	}
	return s.BillingEntitlement(facilityID)
}

type CheckoutIn struct {
	PlanCode       string `json:"planCode"`
	FacilityID     int64  `json:"facilityId"`
	OrganisationID int64  `json:"organisationId"`
	ClientType     string `json:"clientType"`
}

type ConfirmIn struct {
	OrderID        string `json:"orderId"`
	PaymentID      string `json:"paymentId"`
	Signature      string `json:"signature"`
	FacilityID     int64  `json:"facilityId"`
	OrganisationID int64  `json:"organisationId"`
}

func (s *Service) Checkout(ctx context.Context, in CheckoutIn) map[string]interface{} {
	plan := strings.ToLower(strings.TrimSpace(in.PlanCode))
	if plan == "" {
		plan = "pro"
	}
	rupees := planPriceINR(plan)
	if rupees <= 0 {
		// Free plan — activate without Razorpay.
		if in.FacilityID > 0 {
			_, _ = s.DB.Exec(`
INSERT INTO platform_billing_entitlements (facility_id, plan_code, standard_active, paid_active, source, updated_at)
VALUES ($1, $2, true, true, 'free', NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  plan_code=EXCLUDED.plan_code, standard_active=true, paid_active=true, source='free', updated_at=NOW()`,
				in.FacilityID, plan)
		}
		return map[string]interface{}{
			"success": true,
			"message": "free_plan_activated",
			"data": map[string]interface{}{
				"free":     true,
				"planCode": plan,
				"key_id":   "",
				"order_id": "",
				"amount":   0,
				"currency": "INR",
			},
		}
	}

	base := strings.TrimSpace(s.Cfg.PaygatewayURL)
	if base == "" {
		return map[string]interface{}{"success": false, "message": "paygateway_url_missing"}
	}

	body := map[string]interface{}{
		"amount":   rupees,
		"currency": "INR",
		"receipt":  fmt.Sprintf("fac_%d_%s_%d", in.FacilityID, plan, time.Now().Unix()),
		"purpose":  "saas_" + plan,
		"metadata": map[string]interface{}{
			"plan_code": plan,
			"client":    in.ClientType,
		},
	}
	if in.FacilityID > 0 {
		body["facility_id"] = in.FacilityID
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/gateway/orders", bytes.NewReader(raw))
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.PaygatewayKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.PaygatewayKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return map[string]interface{}{"success": false, "message": "paygateway_unreachable"}
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("paygateway_http_%d", res.StatusCode),
			"detail":  string(respBody),
		}
	}
	var gw struct {
		Provider        string                 `json:"provider"`
		ExternalOrderID string                 `json:"external_order_id"`
		AmountPaise     int64                  `json:"amount_paise"`
		Currency        string                 `json:"currency"`
		Checkout        map[string]interface{} `json:"checkout"`
	}
	if err := json.Unmarshal(respBody, &gw); err != nil {
		return map[string]interface{}{"success": false, "message": "paygateway_bad_json"}
	}
	orderID := gw.ExternalOrderID
	keyID := ""
	if gw.Checkout != nil {
		if v, ok := gw.Checkout["key"].(string); ok {
			keyID = v
		}
		if v, ok := gw.Checkout["order_id"].(string); ok && v != "" {
			orderID = v
		}
	}
	if orderID == "" {
		return map[string]interface{}{"success": false, "message": "order_id_missing"}
	}

	_, _ = s.DB.Exec(`
INSERT INTO platform_billing_orders
  (facility_id, organisation_id, plan_code, provider, external_order_id, amount_paise, currency, status)
VALUES (NULLIF($1,0), NULLIF($2,0), $3, $4, $5, $6, $7, 'created')`,
		in.FacilityID, in.OrganisationID, plan, gw.Provider, orderID, gw.AmountPaise, gw.Currency)

	amountRupees := float64(gw.AmountPaise) / 100.0
	if amountRupees <= 0 {
		amountRupees = rupees
	}
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"key_id":   keyID,
			"order_id": orderID,
			"amount":   amountRupees,
			"currency": gw.Currency,
			"provider": gw.Provider,
			"planCode": plan,
		},
	}
}

func (s *Service) ConfirmBilling(ctx context.Context, in ConfirmIn) map[string]interface{} {
	orderID := strings.TrimSpace(in.OrderID)
	if orderID == "" {
		return map[string]interface{}{"success": false, "message": "orderId_required"}
	}

	base := strings.TrimSpace(s.Cfg.PaygatewayURL)
	if base != "" && in.PaymentID != "" {
		body := map[string]interface{}{
			"external_order_id": orderID,
			"payment_id":        in.PaymentID,
			"signature":         in.Signature,
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/gateway/verify", bytes.NewReader(raw))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if s.Cfg.PaygatewayKey != "" {
				req.Header.Set("X-Service-Key", s.Cfg.PaygatewayKey)
			}
			client := &http.Client{Timeout: 15 * time.Second}
			res, err := client.Do(req)
			if err == nil {
				defer res.Body.Close()
				respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				var vr struct {
					Valid  bool   `json:"valid"`
					Status string `json:"status"`
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(respBody, &vr)
				if res.StatusCode >= 300 || !vr.Valid {
					msg := vr.Reason
					if msg == "" {
						msg = "payment_verify_failed"
					}
					return map[string]interface{}{"success": false, "message": msg}
				}
			}
		}
	}

	var fid sql.NullInt64
	var plan string
	_ = s.DB.QueryRow(`
SELECT facility_id, plan_code FROM platform_billing_orders
WHERE external_order_id=$1 ORDER BY id DESC LIMIT 1`, orderID).Scan(&fid, &plan)
	if plan == "" {
		plan = "pro"
	}
	facilityID := in.FacilityID
	if facilityID <= 0 && fid.Valid {
		facilityID = fid.Int64
	}

	_, _ = s.DB.Exec(`
UPDATE platform_billing_orders SET status='paid', payment_id=$1, paid_at=NOW()
WHERE external_order_id=$2`, strings.TrimSpace(in.PaymentID), orderID)

	if facilityID > 0 {
		_, _ = s.DB.Exec(`
INSERT INTO platform_billing_entitlements (facility_id, plan_code, standard_active, paid_active, source, updated_at)
VALUES ($1, $2, true, true, 'paid', NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  plan_code=EXCLUDED.plan_code, standard_active=true, paid_active=true, source='paid', updated_at=NOW()`,
			facilityID, plan)
	}

	out := s.BillingEntitlement(facilityID)
	if data, ok := out["data"].(map[string]interface{}); ok {
		data["status"] = "paid"
		data["order_id"] = orderID
		data["payment_id"] = in.PaymentID
	}
	return out
}
