package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/facinect/app/internal/config"
	"github.com/facinect/app/internal/db"
)

// Same WhatsApp for the end user and the facility must still produce two sends:
// one end-user copy and one facility copy. Cancel uses booking_cancellation only.
func TestSameNumberSendsEndUserAndFacilityWhatsApp(t *testing.T) {
	password := strings.TrimSpace(os.Getenv("DB_PASSWORD"))
	if password == "" {
		if b, err := os.ReadFile(os.Getenv("DB_PASSWORD_FILE")); err == nil {
			password = strings.TrimSpace(string(b))
		}
	}
	if password == "" {
		t.Skip("DB_PASSWORD not set")
	}
	host := getenvTest("DB_HOST", "127.0.0.1")
	port := getenvTest("DB_PORT", "5433")
	user := getenvTest("DB_USER", "identity")
	name := getenvTest("DB_NAME", "fac_identity")
	sqlDB, err := db.Open(host, port, user, password, name)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx := context.Background()
	const phone = "918800221001"
	var facilityID, userID int64
	defer func() {
		cctx := context.Background()
		if userID > 0 {
			_, _ = sqlDB.ExecContext(cctx, `DELETE FROM app_notifications WHERE user_id=$1`, userID)
			_, _ = sqlDB.ExecContext(cctx, `DELETE FROM app_users WHERE id=$1`, userID)
		}
		_, _ = sqlDB.ExecContext(cctx, `DELETE FROM app_users WHERE whatsapp_no=$1`, phone)
		if facilityID > 0 {
			_, _ = sqlDB.ExecContext(cctx, `DELETE FROM facility_profiles WHERE facility_id=$1`, facilityID)
			_, _ = sqlDB.ExecContext(cctx, `DELETE FROM app_facilities WHERE id=$1`, facilityID)
		}
		_, _ = sqlDB.ExecContext(cctx, `DELETE FROM facility_profiles WHERE whatsapp=$1`, phone)
		_, _ = sqlDB.ExecContext(cctx, `DELETE FROM app_facilities WHERE name='WA Same Number Test'`)
		_ = sqlDB.Close()
	}()
	_, _ = sqlDB.ExecContext(ctx, `DELETE FROM app_notifications WHERE user_id IN (SELECT id FROM app_users WHERE whatsapp_no=$1)`, phone)
	_, _ = sqlDB.ExecContext(ctx, `DELETE FROM app_users WHERE whatsapp_no=$1`, phone)
	_, _ = sqlDB.ExecContext(ctx, `DELETE FROM facility_profiles WHERE whatsapp=$1`, phone)
	_, _ = sqlDB.ExecContext(ctx, `DELETE FROM app_facilities WHERE name='WA Same Number Test'`)

	err = sqlDB.QueryRowContext(ctx, `
INSERT INTO app_facilities (name, location, city_name)
VALUES ('WA Same Number Test', 'Test', 'Chennai')
RETURNING id`).Scan(&facilityID)
	if err != nil {
		t.Fatal(err)
	}
	err = sqlDB.QueryRowContext(ctx, `
INSERT INTO app_users (full_name, whatsapp_no, phone, email)
VALUES ('Same Number Tester', $1, $1, '')
RETURNING id`, phone).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlDB.ExecContext(ctx, `
INSERT INTO facility_profiles (facility_id, display_name, whatsapp, phone)
VALUES ($1, 'WA Same Number Test', $2, $2)`, facilityID, phone)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"sent"}`))
	}))
	defer srv.Close()

	svc := &Service{DB: sqlDB, Cfg: config.Config{NotificationsURL: srv.URL}}
	slots := []any{map[string]any{
		"court": "Court 1",
		"date":  "2026-10-23",
		"time":  "06:00-07:00",
	}}
	const bookingID int64 = 880221001
	svc.notifyBookingConfirmation(ctx, userID, facilityID, bookingID, slots, "advance", "TXN_TEST", 100, 0, 100, 0)

	afterConfirm := snapshot(&mu, &got)
	confirm := whatsappTo(whatsappSends(afterConfirm), phone)
	if len(confirm) != 2 {
		t.Fatalf("confirm whatsapp sends = %d, want 2 (end user and facility)", len(confirm))
	}
	for i, body := range confirm {
		if tmpl := strField(body, "template"); tmpl != "booking_confirmation" {
			t.Fatalf("confirm send %d template = %q, want booking_confirmation", i, tmpl)
		}
		if phoneOf(body) != phone {
			t.Fatalf("confirm send %d phone = %q, want %s", i, phoneOf(body), phone)
		}
	}

	slotsJSON, _ := json.Marshal(slots)
	svc.notifyBookingCancelled(ctx, userID, facilityID, bookingID, "WA Same Number Test", slotsJSON, "", 0)

	all := snapshot(&mu, &got)
	cancel := whatsappTo(whatsappSends(all[len(afterConfirm):]), phone)
	if len(cancel) != 2 {
		t.Fatalf("cancel whatsapp sends = %d, want 2 (end user and facility)", len(cancel))
	}
	for i, body := range cancel {
		tmpl := strField(body, "template")
		if tmpl != "booking_cancellation" {
			t.Fatalf("cancel send %d template = %q, want booking_cancellation", i, tmpl)
		}
		if phoneOf(body) != phone {
			t.Fatalf("cancel send %d phone = %q, want %s", i, phoneOf(body), phone)
		}
		data, _ := body["data"].(map[string]any)
		if strField(data, "message_type") == "text" {
			t.Fatalf("cancel send %d used free-form text", i)
		}
	}
}

func snapshot(mu *sync.Mutex, got *[]map[string]any) []map[string]any {
	mu.Lock()
	defer mu.Unlock()
	out := make([]map[string]any, len(*got))
	copy(out, *got)
	return out
}

func whatsappTo(bodies []map[string]any, phone string) []map[string]any {
	out := make([]map[string]any, 0, len(bodies))
	for _, body := range bodies {
		if phoneOf(body) == phone {
			out = append(out, body)
		}
	}
	return out
}

func whatsappSends(bodies []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(bodies))
	for _, body := range bodies {
		if strings.EqualFold(strField(body, "channel"), "whatsapp") {
			out = append(out, body)
		}
	}
	return out
}

func phoneOf(body map[string]any) string {
	to, _ := body["to"].(map[string]any)
	return normalizeWA(strField(to, "whatsapp"))
}

func getenvTest(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
