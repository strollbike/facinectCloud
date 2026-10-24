package notify

import (
	"testing"

	"github.com/facinect/notifications/internal/testreport"
)

func TestPushTargetApp(t *testing.T) {
	t.Run("Staff alert uses StrollX tokens", func(t *testing.T) {
		defer testreport.Done(t, "Staff alert uses StrollX tokens")
		if pushTargetApp(SendRequest{TemplateKey: "booking_staff_notify"}) != "strollx" {
			t.Fatal("staff booking alert must use StrollX tokens")
		}
	})
	t.Run("Customer alert uses Proximity tokens", func(t *testing.T) {
		defer testreport.Done(t, "Customer alert uses Proximity tokens")
		if pushTargetApp(SendRequest{TemplateKey: "booking_user_notify"}) != "proximity" {
			t.Fatal("customer booking alert must use Proximity tokens")
		}
	})
	t.Run("StrollX app flag", func(t *testing.T) {
		defer testreport.Done(t, "StrollX app flag")
		if pushTargetApp(SendRequest{Data: map[string]any{"app": "StrollX"}}) != "strollx" {
			t.Fatal("explicit strollx app flag")
		}
	})
	t.Run("WhatsApp template stays Proximity", func(t *testing.T) {
		defer testreport.Done(t, "WhatsApp template stays Proximity")
		if pushTargetApp(SendRequest{TemplateKey: "booking_confirmation"}) != "proximity" {
			t.Fatal("whatsapp template is not a StrollX push")
		}
	})
}
