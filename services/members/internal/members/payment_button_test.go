package members

import "testing"

func TestPaymentReminderButtonParamAlwaysNonEmpty(t *testing.T) {
	got := paymentReminderButtonParam("7708054914", 2, "test-encryption-key-32bytes!!")
	if got == "" || got == "?p=" {
		t.Fatalf("encrypted button empty: %q", got)
	}
	fallback := paymentReminderButtonParam("7708054914", 2, "")
	// Empty key still encrypts with a zero-padded AES key, or falls back.
	if fallback == "" || fallback == "?p=" {
		t.Fatalf("fallback button empty: %q", fallback)
	}
}
