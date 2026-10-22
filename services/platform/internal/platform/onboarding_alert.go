package platform

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
	"net/url"
	"strings"
)

const onboardingTemplate = "onboarding_pending_review"

func (s *Service) notifyOnboardingPending(ctx context.Context, id int64, kind, email, wa string, payload map[string]interface{}) error {
	approve, err := s.encryptOnboardingLink(fmt.Sprintf("action=approve&id=%d", id))
	if err != nil {
		log.Printf("onboarding alert encrypt approve: %v", err)
		return err
	}
	view, err := s.encryptOnboardingLink(fmt.Sprintf("action=view&kind=%s&id=%d", kind, id))
	if err != nil {
		log.Printf("onboarding alert encrypt view: %v", err)
		return err
	}
	name := onboardingDisplayName(kind, payload)
	if name == "" {
		name = "-"
	}
	if email == "" {
		email = "-"
	}
	if wa == "" {
		wa = "-"
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return fmt.Errorf("invalid_kind")
	}
	typeLabel := strings.ToUpper(kind[:1]) + kind[1:]
	rows, err := s.DB.QueryContext(ctx, `
SELECT whatsapp FROM platform_alert_contacts
WHERE purpose='onboarding_pending' AND active=TRUE AND trim(whatsapp) <> ''
ORDER BY id`)
	if err != nil {
		log.Printf("onboarding alert contacts: %v", err)
		return err
	}
	defer rows.Close()
	var phones []string
	for rows.Next() {
		var phone string
		if rows.Scan(&phone) == nil && strings.TrimSpace(phone) != "" {
			phones = append(phones, strings.TrimSpace(phone))
		}
	}
	if len(phones) == 0 {
		log.Printf("onboarding alert: no active contacts for request %d", id)
		return fmt.Errorf("no_alert_contacts")
	}
	sent := 0
	var last error
	for _, phone := range phones {
		if err := s.sendOnboardingTemplate(ctx, phone, typeLabel, name, wa, email, id, approve, view); err != nil {
			log.Printf("onboarding alert send %s: %v", phone, err)
			last = err
			continue
		}
		sent++
	}
	if sent == 0 {
		if last == nil {
			last = fmt.Errorf("send_failed")
		}
		return last
	}
	return nil
}

// SendPendingOnboardingAlert resends onboarding_pending_review for an existing pending request.
func (s *Service) SendPendingOnboardingAlert(ctx context.Context, kind, email, wa string, id int64) (map[string]interface{}, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "tournaments" {
		kind = "tournament"
	}
	switch kind {
	case "facility", "tournament", "referee":
	default:
		return nil, fmt.Errorf("invalid_kind")
	}
	email = normalizeEmail(email)
	wa = normalizeWhatsApp(wa)
	if id <= 0 && email == "" && wa == "" {
		return nil, fmt.Errorf("request_required")
	}

	var rowID int64
	var rowKind, rowEmail, rowWa, status string
	var payload []byte
	var err error
	if id > 0 {
		err = s.DB.QueryRowContext(ctx, `
SELECT id, kind, COALESCE(email,''), COALESCE(whatsapp_number,''), status, COALESCE(payload,'{}'::jsonb)
FROM platform_onboarding_requests WHERE id=$1`, id).
			Scan(&rowID, &rowKind, &rowEmail, &rowWa, &status, &payload)
	} else {
		err = s.DB.QueryRowContext(ctx, `
SELECT id, kind, COALESCE(email,''), COALESCE(whatsapp_number,''), status, COALESCE(payload,'{}'::jsonb)
FROM platform_onboarding_requests
WHERE kind=$1 AND status IN ('pending','submitted')
  AND (
    ($2 <> '' AND lower(trim(COALESCE(email,''))) = $2)
    OR ($3 <> '' AND regexp_replace(COALESCE(whatsapp_number,''), '\D', '', 'g') = $3)
  )
ORDER BY id DESC
LIMIT 1`, kind, email, wa).
			Scan(&rowID, &rowKind, &rowEmail, &rowWa, &status, &payload)
	}
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not_found")
	}
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(rowKind), kind) {
		return nil, fmt.Errorf("not_found")
	}
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "pending" && st != "submitted" {
		return nil, fmt.Errorf("not_pending")
	}
	if !onboardingIdentityMatches(rowEmail, rowWa, email, wa) {
		return nil, fmt.Errorf("not_found")
	}
	var payloadMap map[string]interface{}
	_ = json.Unmarshal(payload, &payloadMap)
	if payloadMap == nil {
		payloadMap = map[string]interface{}{}
	}
	if strings.TrimSpace(rowEmail) == "" {
		rowEmail = email
	}
	if strings.TrimSpace(rowWa) == "" {
		rowWa = wa
	}
	if err := s.notifyOnboardingPending(ctx, rowID, rowKind, rowEmail, rowWa, payloadMap); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"success": true,
		"message": "Approval request sent on WhatsApp.",
		"data": map[string]interface{}{
			"id":     rowID,
			"kind":   rowKind,
			"status": "pending",
		},
	}, nil
}

func onboardingIdentityMatches(rowEmail, rowWa, email, wa string) bool {
	if email != "" && strings.EqualFold(strings.TrimSpace(rowEmail), email) {
		return true
	}
	rowDigits := normalizeWhatsApp(rowWa)
	if wa != "" && rowDigits != "" && rowDigits == wa {
		return true
	}
	return false
}

func (s *Service) sendOnboardingTemplate(ctx context.Context, phone, typeLabel, name, wa, email string, id int64, approve, view string) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	payload := map[string]interface{}{
		"channel":  "whatsapp",
		"template": onboardingTemplate,
		"to":       map[string]interface{}{"whatsapp": phone},
		"data": map[string]interface{}{
			"language": "en",
			"body_params": []string{
				typeLabel, name, wa, email, fmt.Sprintf("%d", id),
			},
			"button_url_params": []string{approve, view},
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
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notifications_http_%d %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ApproveOnboardingLink decrypts the Approve button and marks that request approved.
func (s *Service) ApproveOnboardingLink(ctx context.Context, token string) (string, int) {
	values, err := s.decryptOnboardingLink(token)
	if err != nil || values.Get("action") != "approve" {
		return onboardingPage("Could not approve", "This approval link is not valid."), http.StatusBadRequest
	}
	id, err := parsePositiveID(values.Get("id"))
	if err != nil {
		return onboardingPage("Could not approve", "This approval link is not valid."), http.StatusBadRequest
	}
	var kind, email, wa, status string
	var payload []byte
	err = s.DB.QueryRowContext(ctx, `
SELECT kind, COALESCE(email,''), COALESCE(whatsapp_number,''), status, payload
FROM platform_onboarding_requests WHERE id=$1`, id).Scan(&kind, &email, &wa, &status, &payload)
	if err == sql.ErrNoRows {
		return onboardingPage("Not found", "This onboarding request does not exist."), http.StatusNotFound
	}
	if err != nil {
		return onboardingPage("Could not approve", "Try again from the management page."), http.StatusInternalServerError
	}
	if err := s.approveOnboardingID(ctx, id, kind, email, wa, status, payload); err != nil {
		if err.Error() == "already_approved" {
			return "", http.StatusOK
		}
		log.Printf("onboarding approve %d: %v", id, err)
		return onboardingPage("Could not approve", err.Error()), http.StatusInternalServerError
	}
	return "", http.StatusOK
}

func (s *Service) approveOnboardingID(ctx context.Context, id int64, kind, email, wa, status string, payload []byte) error {
	if strings.EqualFold(status, "approved") {
		return fmt.Errorf("already_approved")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE platform_onboarding_requests SET status='approved' WHERE id=$1 AND status<>'approved'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("already_approved")
	}
	if strings.EqualFold(kind, "facility") {
		var payloadMap map[string]interface{}
		_ = json.Unmarshal(payload, &payloadMap)
		if _, _, perr := s.provisionApprovedFacility(ctx, email, wa, payloadMap); perr != nil {
			return fmt.Errorf("approved, facility setup needs a check: %s", perr.Error())
		}
	}
	return nil
}

// ApproveOnboardingID approves one request from the management page.
func (s *Service) ApproveOnboardingID(ctx context.Context, id int64) error {
	var kind, email, wa, status string
	var payload []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT kind, COALESCE(email,''), COALESCE(whatsapp_number,''), status, payload
FROM platform_onboarding_requests WHERE id=$1`, id).Scan(&kind, &email, &wa, &status, &payload)
	if err != nil {
		return err
	}
	err = s.approveOnboardingID(ctx, id, kind, email, wa, status, payload)
	if err != nil && err.Error() == "already_approved" {
		return nil
	}
	return err
}

// OnboardingManagePage renders every facility, tournament, and referee request.
func (s *Service) OnboardingManagePage(ctx context.Context, focus int64, notice string) (string, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, kind, COALESCE(email,''), COALESCE(whatsapp_number,''), status,
       COALESCE(payload,'{}'::jsonb), to_char(created_at, 'YYYY-MM-DD HH24:MI')
FROM platform_onboarding_requests
ORDER BY id DESC
LIMIT 300`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var body strings.Builder
	count := 0
	for rows.Next() {
		var id int64
		var kind, email, wa, status, created string
		var payload []byte
		if err := rows.Scan(&id, &kind, &email, &wa, &status, &payload, &created); err != nil {
			return "", err
		}
		var payloadMap map[string]interface{}
		_ = json.Unmarshal(payload, &payloadMap)
		name := onboardingDisplayName(kind, payloadMap)
		if name == "" {
			name = "-"
		}
		count++
		rowClass := "row"
		if focus > 0 && id == focus {
			rowClass = "row focus"
		}
		approve := ""
		if !strings.EqualFold(status, "approved") {
			approve = fmt.Sprintf(`<form method="post" action="/v1/onboarding/manage/approve"><input type="hidden" name="id" value="%d"><button type="submit">Approve</button></form>`, id)
		}
		fmt.Fprintf(&body, `<tr class="%s"><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><span class="st %s">%s</span></td><td>%s</td><td>%s</td></tr>`,
			rowClass, id, htmlEscape(titleKind(kind)), htmlEscape(name), htmlEscape(wa), htmlEscape(email),
			htmlEscape(strings.ToLower(status)), htmlEscape(status), htmlEscape(created), approve)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	note := ""
	if strings.TrimSpace(notice) != "" {
		note = `<p class="note">` + htmlEscape(notice) + `</p>`
	}
	empty := ""
	if count == 0 {
		empty = `<p class="empty">No onboarding requests yet.</p>`
	}
	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Onboarding management</title>
<style>
body{font-family:sans-serif;margin:0;background:#f6f7fb;color:#14213d}
header{padding:20px 16px 8px} h1{margin:0;font-size:22px}
.note{margin:12px 16px;padding:10px 12px;background:#ecfdf5;border-radius:10px}
.wrap{overflow:auto;padding:8px 16px 28px}
table{width:100%%;border-collapse:collapse;background:#fff;border-radius:12px}
th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #e8ecf4;font-size:14px;vertical-align:top}
th{font-size:12px;color:#64748b;text-transform:uppercase}
tr.focus{background:#eff6ff}
.st{padding:2px 8px;border-radius:999px;background:#fef3c7;color:#92400e;font-weight:700;font-size:12px}
.st.approved{background:#dcfce7;color:#166534}
.st.rejected{background:#fee2e2;color:#991b1b}
button{background:#16a34a;color:#fff;border:0;border-radius:8px;padding:8px 12px;font-weight:700}
.empty{padding:16px;color:#64748b}
</style></head><body>
<header><h1>Onboarding</h1><p>%d request(s)</p></header>
%s
<div class="wrap">%s<table><thead><tr><th>ID</th><th>Type</th><th>Name</th><th>WhatsApp</th><th>Email</th><th>Status</th><th>Created</th><th></th></tr></thead><tbody>%s</tbody></table></div>
</body></html>`, count, note, empty, body.String()), nil
}

func titleKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "-"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// ViewOnboardingLink decrypts the View button and returns the in-service management page.
func (s *Service) ViewOnboardingLink(token string) (string, error) {
	values, err := s.decryptOnboardingLink(token)
	if err != nil || values.Get("action") != "view" {
		return "", fmt.Errorf("invalid_link")
	}
	id, err := parsePositiveID(values.Get("id"))
	if err != nil {
		return "", fmt.Errorf("invalid_link")
	}
	return fmt.Sprintf("/v1/onboarding/manage?focus=%d", id), nil
}

func onboardingDisplayName(kind string, payload map[string]interface{}) string {
	switch kind {
	case "facility":
		return strings.TrimSpace(payloadString(payload, "facilityName", "name", "display_name"))
	case "tournament":
		return strings.TrimSpace(payloadString(payload, "organisationName", "organizationName", "name", "facilityName"))
	case "referee":
		return strings.TrimSpace(payloadString(payload, "fullName", "name", "refereeName"))
	default:
		return strings.TrimSpace(payloadString(payload, "name"))
	}
}

func (s *Service) encryptOnboardingLink(plain string) (string, error) {
	key := strings.TrimSpace(s.Cfg.EncryptionKey)
	if key == "" {
		return "", fmt.Errorf("encryption_key_missing")
	}
	out := encryptURLParam(plain, key)
	if out == "" {
		return "", fmt.Errorf("encrypt_failed")
	}
	return out, nil
}

func (s *Service) decryptOnboardingLink(token string) (url.Values, error) {
	key := strings.TrimSpace(s.Cfg.EncryptionKey)
	if key == "" {
		return nil, fmt.Errorf("encryption_key_missing")
	}
	plain, err := decryptURLParam(token, key)
	if err != nil {
		return nil, err
	}
	return url.ParseQuery(plain)
}

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

func decryptURLParam(token, key string) (string, error) {
	s := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(token))
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	packed, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	parts := bytes.SplitN(packed, []byte("::"), 2)
	if len(parts) != 2 || len(parts[1]) != aes.BlockSize {
		return "", fmt.Errorf("bad_token")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(string(parts[0]))
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("bad_token")
	}
	k := make([]byte, 32)
	copy(k, []byte(key))
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, parts[1]).CryptBlocks(plain, ciphertext)
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(plain), nil
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

func pkcs7Unpad(src []byte, blockSize int) ([]byte, error) {
	if len(src) == 0 || len(src)%blockSize != 0 {
		return nil, fmt.Errorf("bad_padding")
	}
	pad := int(src[len(src)-1])
	if pad <= 0 || pad > blockSize || pad > len(src) {
		return nil, fmt.Errorf("bad_padding")
	}
	for _, b := range src[len(src)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("bad_padding")
		}
	}
	return src[:len(src)-pad], nil
}

func parsePositiveID(raw string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(strings.TrimSpace(raw), &id)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid_id")
	}
	return id, nil
}

func onboardingPage(title, message string) string {
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head><body style="font-family:sans-serif;padding:24px"><h1>%s</h1><p>%s</p></body></html>`,
		htmlEscape(title), htmlEscape(title), htmlEscape(message))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
