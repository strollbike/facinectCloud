package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/facinect/notifications/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type PushClient struct {
	Cfg    config.Config
	Client *http.Client

	mu          sync.Mutex
	tokenSource oauth2.TokenSource
	projectID   string
}

func NewPush(cfg config.Config) *PushClient {
	return &PushClient{
		Cfg: cfg,
		Client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (p *PushClient) Send(ctx context.Context, req SendRequest) (string, error) {
	tokens := req.To.DeviceTokens
	if len(tokens) == 0 {
		return "", fmt.Errorf("device_token_required")
	}
	title := "Facinect"
	body := strings.TrimSpace(req.TemplateKey)
	if body == "" {
		body = "You have a new notification"
	}
	stringData := map[string]string{}
	resolvedTmpl, _ := resolveWhatsAppTemplate(req.TemplateKey, "")
	if params := bodyParamStrings(req.Data); len(params) >= 3 &&
		resolvedTmpl == facilityDynamicTemplate {
		title = params[0]
		body = FacilityDynamicMessage(params[0], params[1], params[2])
	} else if req.Data != nil {
		if t, ok := req.Data["title"].(string); ok && t != "" {
			title = t
		}
		if b, ok := req.Data["body"].(string); ok && b != "" {
			body = b
		}
	}
	if req.Data != nil {
		for k, v := range req.Data {
			if !fcmDataKeyAllowed(k) {
				continue
			}
			stringData[k] = fmt.Sprint(v)
		}
	}

	if p.Cfg.DryRun || !p.Cfg.PushConfigured() {
		return fmt.Sprintf("dry_run:push:%d", len(tokens)), nil
	}

	// Prefer FCM HTTP v1 (service account). Fall back to legacy server key.
	if p.Cfg.FCMServiceAccountJSON != "" {
		ok, fail := 0, 0
		var lastErr error
		for _, tok := range tokens {
			if err := p.sendV1(ctx, tok, title, body, stringData); err != nil {
				fail++
				lastErr = err
				continue
			}
			ok++
		}
		if ok == 0 {
			if lastErr != nil {
				return "", lastErr
			}
			return "", fmt.Errorf("fcm_v1_all_failed")
		}
		return fmt.Sprintf("fcm_v1:ok:%d:fail:%d", ok, fail), nil
	}

	return p.sendLegacy(ctx, tokens, title, body, req.Data)
}

func (p *PushClient) ensureV1(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tokenSource != nil && p.projectID != "" {
		return nil
	}
	path := strings.TrimSpace(p.Cfg.FCMServiceAccountJSON)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fcm_sa_read: %w", err)
	}
	var meta struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal(raw, &meta)
	projectID := strings.TrimSpace(p.Cfg.FCMProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(meta.ProjectID)
	}
	if projectID == "" {
		return fmt.Errorf("fcm_project_id_missing")
	}
	creds, err := google.CredentialsFromJSON(ctx, raw, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return fmt.Errorf("fcm_sa_creds: %w", err)
	}
	p.tokenSource = creds.TokenSource
	p.projectID = projectID
	return nil
}

func (p *PushClient) sendV1(ctx context.Context, deviceToken, title, body string, data map[string]string) error {
	if err := p.ensureV1(ctx); err != nil {
		return err
	}
	tok, err := p.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("fcm_oauth: %w", err)
	}
	priority := "HIGH"
	notifPriority := "PRIORITY_HIGH"
	if data != nil && strings.EqualFold(strings.TrimSpace(data["android_priority"]), "normal") {
		priority = "NORMAL"
		notifPriority = "PRIORITY_DEFAULT"
	}
	if data != nil {
		for k := range data {
			if !fcmDataKeyAllowed(k) {
				delete(data, k)
			}
		}
	}
	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"token": deviceToken,
			"notification": map[string]string{
				"title": title,
				"body":  body,
			},
			"data": data,
			"android": map[string]interface{}{
				"priority": priority,
				"notification": map[string]interface{}{
					"channel_id":              androidChannelID(data),
					"notification_priority":   notifPriority,
					"default_sound":           true,
					"default_vibrate_timings": true,
				},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", p.projectID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json; charset=UTF-8")
	res, err := p.Client.Do(httpReq)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("fcm_v1_%d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// fcmDataKeyAllowed drops keys FCM rejects. message_type is reserved
// (Notify/Coupon use it only to choose WhatsApp free text).
func fcmDataKeyAllowed(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" || k == "from" || k == "message_type" || k == "android_priority" {
		return false
	}
	if strings.HasPrefix(k, "google") || strings.HasPrefix(k, "gcm") {
		return false
	}
	return true
}

func androidChannelID(data map[string]string) string {
	t := strings.ToLower(strings.TrimSpace(data["type"]))
	if strings.HasPrefix(t, "booking") {
		return "facinect_booking_v3"
	}
	if strings.HasPrefix(t, "geofence") {
		return "facinect_geofence_v1"
	}
	return "facinect_facility_v3"
}

func (p *PushClient) sendLegacy(ctx context.Context, tokens []string, title, body string, data map[string]interface{}) (string, error) {
	safe := map[string]string{}
	for k, v := range data {
		if !fcmDataKeyAllowed(k) {
			continue
		}
		safe[k] = fmt.Sprint(v)
	}
	payload := map[string]interface{}{
		"registration_ids": tokens,
		"notification": map[string]string{
			"title": title,
			"body":  body,
		},
		"data": safe,
	}
	raw, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fcm.googleapis.com/fcm/send", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "key="+p.Cfg.FCMServerKey)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := p.Client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("fcm_api_%d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return "fcm:ok", nil
}
