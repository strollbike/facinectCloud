package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/facinect/notifications/internal/config"
)

type WhatsAppClient struct {
	Cfg    config.Config
	Client *http.Client
}

type WhatsAppSendCreds struct {
	AccessToken   string
	PhoneNumberID string
}

func NewWhatsApp(cfg config.Config) *WhatsAppClient {
	return &WhatsAppClient{
		Cfg: cfg,
		Client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (w *WhatsAppClient) Send(ctx context.Context, req SendRequest, creds WhatsAppSendCreds) (string, error) {
	to := normalizePhone(req.To.WhatsApp)
	if to == "" {
		return "", fmt.Errorf("whatsapp_required")
	}
	tmpl := strings.TrimSpace(req.TemplateKey)
	if tmpl == "" {
		tmpl = "hello_world1" // Facinect PHP marketing / default template
	}
	lang := "en"
	if req.Data != nil {
		if v, ok := req.Data["language"].(string); ok && strings.TrimSpace(v) != "" {
			lang = strings.TrimSpace(v)
		}
	}
	// Map legacy / app-facing names onto templates approved on this WABA.
	tmpl, lang = resolveWhatsAppTemplate(tmpl, lang)

	token := strings.TrimSpace(creds.AccessToken)
	phoneID := strings.TrimSpace(creds.PhoneNumberID)
	if token == "" {
		token = w.Cfg.MetaWhatsAppToken
	}
	if phoneID == "" {
		phoneID = w.Cfg.MetaPhoneNumberID
	}

	if w.Cfg.DryRun || token == "" || phoneID == "" {
		return fmt.Sprintf("dry_run:whatsapp:%s:%s:phone=%s", to, tmpl, phoneID), nil
	}

	url := fmt.Sprintf(
		"https://graph.facebook.com/%s/%s/messages",
		w.Cfg.MetaAPIVersion,
		phoneID,
	)

	// Session / free-form text (PHP sendWhatsappMessage cancel notice).
	sendAsText := strings.EqualFold(tmpl, "text") ||
		strings.EqualFold(tmpl, "_text") ||
		strings.EqualFold(dataString(req.Data, "message_type", "send_as"), "text")
	textBody := dataString(req.Data, "message", "text", "body")
	var body map[string]interface{}
	if sendAsText && textBody != "" {
		body = map[string]interface{}{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "text",
			"text":              map[string]interface{}{"body": textBody},
		}
	} else {
		template := map[string]interface{}{
			"name": tmpl,
			"language": map[string]string{
				"code": lang,
			},
		}
		if comps := buildTemplateComponents(req); len(comps) > 0 {
			template["components"] = comps
		}
		body = map[string]interface{}{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "template",
			"template":          template,
		}
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := w.Client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("whatsapp_api_%d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed map[string]interface{}
	_ = json.Unmarshal(respBody, &parsed)
	if msgs, ok := parsed["messages"].([]interface{}); ok && len(msgs) > 0 {
		if m, ok := msgs[0].(map[string]interface{}); ok {
			if id, ok := m["id"].(string); ok {
				return id, nil
			}
		}
	}
	return "whatsapp:ok", nil
}

// DataForRecipient copies the shared bulk payload and puts this person's name in {{2}}.
func DataForRecipient(data map[string]interface{}, name string) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range data {
		out[k] = v
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return out
	}
	switch list := out["body_params"].(type) {
	case []interface{}:
		cp := make([]interface{}, len(list))
		copy(cp, list)
		if len(cp) >= 2 {
			cp[1] = name
		}
		out["body_params"] = cp
	case []string:
		cp := append([]string{}, list...)
		if len(cp) >= 2 {
			cp[1] = name
		}
		out["body_params"] = cp
	}
	return out
}

// FacilityDynamicMessage is the approved facinect_facility_dynamic_msg wording.
// {{1}} facility, {{2}} person, {{3}} the typed note.
func FacilityDynamicMessage(facility, person, note string) string {
	facility = strings.TrimSpace(facility)
	person = strings.TrimSpace(person)
	note = strings.TrimSpace(note)
	if facility == "" {
		facility = "Facinect"
	}
	if person == "" {
		person = "there"
	}
	return fmt.Sprintf("Welcome to %s!\n\nHello %s,\n\n%s\n\nThank you for choosing us!", facility, person, note)
}

func bodyParamStrings(data map[string]interface{}) []string {
	if data == nil {
		return nil
	}
	raw, ok := data["body_params"]
	if !ok || raw == nil {
		return nil
	}
	var out []string
	switch list := raw.(type) {
	case []interface{}:
		for _, item := range list {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" && s != "<nil>" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func dataString(data map[string]interface{}, keys ...string) string {
	if data == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := data[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

// facilityDynamicTemplate is the approved dashboard message:
// Welcome to {{1}}! Hello {{2}}, {{3}} Thank you for choosing us!
// {{1}} facility or academy, {{2}} that person's name, {{3}} the typed note.
const facilityDynamicTemplate = "facinect_facility_dynamic_msg"

// resolveWhatsAppTemplate maps logical template keys to Meta-approved names/languages.
// payment_reminder_dns is the Facinect PHP billing template (language en, 3 body params).
func resolveWhatsAppTemplate(name, lang string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "payment_reminder_dns":
		if lang == "" {
			return name, "en"
		}
		return name, lang
	case "payment_reminder", "payment_notice":
		return "payment_notice_v3", "en_US"
	case "member_broadcast", "student_broadcast", "booking_message", "facility_broadcast", "news", "news_v3", facilityDynamicTemplate:
		// Meta approved this template as English (en), not English (US).
		return facilityDynamicTemplate, "en"
	case "hello_world1":
		if lang == "" || lang == "en" {
			return name, "en"
		}
		return name, lang
	case "onboarding_pending_review":
		if lang == "" {
			return name, "en"
		}
		return name, lang
	default:
		return name, lang
	}
}

// buildTemplateComponents mirrors Facinect PHP OTP + body/button/header templates.
func buildTemplateComponents(req SendRequest) []interface{} {
	otp := dataString(req.Data, "otp", "code", "otp_code")
	// OTP-only path when no explicit body_params (keep facinect_otp shape).
	if otp != "" && req.Data["body_params"] == nil {
		return []interface{}{
			map[string]interface{}{
				"type": "body",
				"parameters": []map[string]string{
					{"type": "text", "text": otp},
				},
			},
			map[string]interface{}{
				"type":     "button",
				"sub_type": "url",
				"index":    "0",
				"parameters": []map[string]string{
					{"type": "text", "text": otp},
				},
			},
		}
	}

	var comps []interface{}

	// Header image (hello_world1 marketing).
	if img := dataString(req.Data, "header_image", "header_image_link", "image_link"); img != "" {
		comps = append(comps, map[string]interface{}{
			"type": "header",
			"parameters": []map[string]interface{}{
				{"type": "image", "image": map[string]string{"link": img}},
			},
		})
	}

	// Explicit body_params: ["a","b"]
	if raw, ok := req.Data["body_params"]; ok {
		params := []map[string]string{}
		switch list := raw.(type) {
		case []interface{}:
			for _, item := range list {
				s := strings.TrimSpace(fmt.Sprint(item))
				if s != "" && s != "<nil>" {
					params = append(params, map[string]string{"type": "text", "text": s})
				}
			}
		case []string:
			for _, s := range list {
				s = strings.TrimSpace(s)
				if s != "" {
					params = append(params, map[string]string{"type": "text", "text": s})
				}
			}
		}
		if len(params) > 0 {
			comps = append(comps, map[string]interface{}{"type": "body", "parameters": params})
		}
	} else if body := dataString(req.Data, "body", "text", "message"); body != "" {
		comps = append(comps, map[string]interface{}{
			"type": "body",
			"parameters": []map[string]string{
				{"type": "text", "text": body},
			},
		})
	}

	// Several URL buttons. Index 0 is Approve, index 1 is View.
	if raw, ok := req.Data["button_url_params"]; ok {
		var parts []string
		switch list := raw.(type) {
		case []interface{}:
			for _, item := range list {
				s := strings.TrimSpace(fmt.Sprint(item))
				if s != "" && s != "<nil>" {
					parts = append(parts, s)
				}
			}
		case []string:
			for _, s := range list {
				s = strings.TrimSpace(s)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
		for i, part := range parts {
			comps = append(comps, map[string]interface{}{
				"type":     "button",
				"sub_type": "url",
				"index":    fmt.Sprintf("%d", i),
				"parameters": []map[string]string{
					{"type": "text", "text": part},
				},
			})
		}
	}

	// URL button dynamic suffix (tournament_registration_*, payment_reminder_dns).
	if urlParam := dataString(req.Data, "button_url_param", "url_button_param", "button_param"); urlParam != "" && req.Data["button_url_params"] == nil {
		idx := dataString(req.Data, "button_url_index", "button_index")
		if idx == "" {
			idx = "0"
		}
		comps = append(comps, map[string]interface{}{
			"type":     "button",
			"sub_type": "url",
			"index":    idx,
			"parameters": []map[string]string{
				{"type": "text", "text": urlParam},
			},
		})
	}

	// Quick-reply payload (hello_world1).
	if payload := dataString(req.Data, "quick_reply_payload", "button_payload"); payload != "" {
		idx := dataString(req.Data, "quick_reply_index")
		if idx == "" {
			idx = "1"
		}
		comps = append(comps, map[string]interface{}{
			"type":     "button",
			"sub_type": "quick_reply",
			"index":    idx,
			"parameters": []map[string]string{
				{"type": "payload", "payload": payload},
			},
		})
	}

	return comps
}
