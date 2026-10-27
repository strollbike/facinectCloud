package notify

import "testing"

func TestPaymentReminderDNSRequiresURLButton(t *testing.T) {
	req := SendRequest{
		TemplateKey: "payment_reminder_dns",
		Data: map[string]interface{}{
			"language":    "en",
			"body_params": []string{"Student: Raja", "1200", "2026-10"},
		},
	}
	comps := buildTemplateComponents(req)
	if templateHasURLButton(comps) {
		t.Fatal("expected no URL button without button_url_param")
	}

	req.Data["button_url_param"] = "?p=test-token"
	comps = buildTemplateComponents(req)
	if !templateHasURLButton(comps) {
		t.Fatal("expected URL button when button_url_param is set")
	}
}
