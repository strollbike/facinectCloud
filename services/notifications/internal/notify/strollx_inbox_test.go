package notify

import (
	"testing"

	"github.com/facinect/notifications/internal/testreport"
)

func TestStrollxInboxOnlySkipsProximityPush(t *testing.T) {
	t.Run("Staff alert stays in StrollX", func(t *testing.T) {
		defer testreport.Done(t, "Staff alert stays in StrollX")
		if !strollxInboxOnly(SendRequest{TemplateKey: "booking_staff_notify"}) {
			t.Fatal("staff booking alert must stay in StrollX")
		}
	})
	t.Run("Customer alert is separate", func(t *testing.T) {
		defer testreport.Done(t, "Customer alert is separate")
		if strollxInboxOnly(SendRequest{TemplateKey: "booking_user_notify"}) {
			t.Fatal("customer booking alert is not a StrollX staff inbox row")
		}
	})
	t.Run("StrollX app flag", func(t *testing.T) {
		defer testreport.Done(t, "StrollX app flag")
		if !strollxInboxOnly(SendRequest{Data: map[string]any{"app": "StrollX"}}) {
			t.Fatal("explicit strollx app flag")
		}
	})
	t.Run("WhatsApp still sends", func(t *testing.T) {
		defer testreport.Done(t, "WhatsApp still sends")
		if strollxInboxOnly(SendRequest{TemplateKey: "booking_confirmation"}) {
			t.Fatal("whatsapp template must still send")
		}
	})
}
