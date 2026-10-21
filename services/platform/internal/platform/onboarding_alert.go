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

func (s *Service) notifyOnboardingPending(ctx context.Context, id int64, kind, email, wa string, payload map[string]interface{}) {
	approve, err := s.encryptOnboardingLink(fmt.Sprintf("action=approve&id=%d", id))
	if err != nil {
		log.Printf("onboarding alert encrypt approve: %v", err)
		return
	}
	view, err := s.encryptOnboardingLink(fmt.Sprintf("action=view&kind=%s&id=%d", kind, id))
	if err != nil {
		log.Printf("onboarding alert encrypt view: %v", err)
		return
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
	typeLabel := strings.ToUpper(kind[:1]) + kind[1:]
	rows, err := s.DB.QueryContext(ctx, `
SELECT whatsapp FROM platform_alert_contacts
WHERE purpose='onboarding_pending' AND active=TRUE AND trim(whatsapp) <> ''
ORDER BY id`)
	if err != nil {
		log.Printf("onboarding alert contacts: %v", err)
		return
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
		return
	}
	for _, phone := range phones {
		if err := s.sendOnboardingTemplate(ctx, phone, typeLabel, name, wa, email, id, approve, view); err != nil {
			log.Printf("onboarding alert send %s: %v", phone, err)
		}
	}
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
	if strings.EqualFold(status, "approved") {
		return onboardingPage("Already approved", "This request was already approved."), http.StatusOK
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE platform_onboarding_requests SET status='approved' WHERE id=$1 AND status<>'approved'`, id)
	if err != nil {
		return onboardingPage("Could not approve", "Try again from the management page."), http.StatusInternalServerError
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return onboardingPage("Already approved", "This request was already approved."), http.StatusOK
	}
	if strings.EqualFold(kind, "facility") {
		var payloadMap map[string]interface{}
		_ = json.Unmarshal(payload, &payloadMap)
		if _, _, perr := s.provisionApprovedFacility(ctx, email, wa, payloadMap); perr != nil {
			log.Printf("onboarding approve provision %d: %v", id, perr)
			return onboardingPage("Approved with a warning", "The request is approved. Facility setup needs a check: "+perr.Error()), http.StatusOK
		}
	}
	return onboardingPage("Approved", fmt.Sprintf("%s request %d is approved.", kind, id)), http.StatusOK
}

// ViewOnboardingLink decrypts the View button and returns the management page URL.
func (s *Service) ViewOnboardingLink(token string) (string, error) {
	values, err := s.decryptOnboardingLink(token)
	if err != nil || values.Get("action") != "view" {
		return "", fmt.Errorf("invalid_link")
	}
	kind := strings.ToLower(strings.TrimSpace(values.Get("kind")))
	switch kind {
	case "facility":
		return "https://facinect.com/Services/Facinect/strollusers/facility_onboarding.php", nil
	case "tournament":
		return "https://facinect.com/Services/Facinect/strollusers/tournament_onboarding.php", nil
	case "referee":
		return "https://facinect.com/Services/Facinect/strollusers/referee_onboarding.php", nil
	default:
		return "", fmt.Errorf("invalid_kind")
	}
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
