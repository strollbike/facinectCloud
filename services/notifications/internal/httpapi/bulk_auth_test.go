package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/facinect/notifications/internal/authn"
	"github.com/facinect/notifications/internal/config"
	"github.com/facinect/notifications/internal/testreport"
	"github.com/golang-jwt/jwt/v5"
)

func TestStaffMayNotify(t *testing.T) {
	defer testreport.Done(t, "Class-over roles that may notify")
	for _, role := range []string{"admin", "sub_admin", "subadmin", "coach", "headcoach", "head_coach", " Coach "} {
		if !staffMayNotify(role) {
			t.Fatalf("expected %q to notify", role)
		}
	}
	for _, role := range []string{"", "member", "student", "customer"} {
		if staffMayNotify(role) {
			t.Fatalf("did not expect %q to notify", role)
		}
	}
}

func TestStaffBulkStatusUsesLoginRole(t *testing.T) {
	defer testreport.Done(t, "Class-over send uses the login role")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "public.pem")
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := authn.NewVerifier(pubPath, "https://api.facinect.local", "facinect-apps")
	if err != nil {
		t.Fatal(err)
	}
	role := "coach"
	s := &Server{
		Cfg: config.Config{AppEnv: "production", ServiceKey: "server-secret"},
		JWT: verifier,
		lookupRole: func(_ context.Context, userID, facilityID int64) (string, error) {
			if userID != 9 || facilityID != 1 {
				t.Fatalf("lookup user=%d facility=%d", userID, facilityID)
			}
			return role, nil
		},
	}
	body := []byte(`{"facility_id":1,"channel":"both"}`)
	req := httpRequest(t, signAccess(t, key, "9"), body)
	if status, code := s.staffBulkStatus(req, body); status != 0 || code != "" {
		t.Fatalf("coach: status=%d code=%s", status, code)
	}

	role = "member"
	req = httpRequest(t, signAccess(t, key, "9"), body)
	if status, code := s.staffBulkStatus(req, body); status != http.StatusForbidden || code != "forbidden_facility" {
		t.Fatalf("member: status=%d code=%s", status, code)
	}

	req = httpRequest(t, signAccess(t, key, "9"), []byte(`{}`))
	if status, code := s.staffBulkStatus(req, []byte(`{}`)); status != http.StatusBadRequest || code != "facility_id_required" {
		t.Fatalf("missing facility: status=%d code=%s", status, code)
	}

	req = httpRequest(t, "not-a-token", body)
	if status, code := s.staffBulkStatus(req, body); status != http.StatusUnauthorized || code != "unauthorized" {
		t.Fatalf("bad token: status=%d code=%s", status, code)
	}

	if !s.authorized(serviceKeyRequest(t)) {
		t.Fatal("service key should still authorize")
	}
}

func httpRequest(t *testing.T, token string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/notifications/send-bulk", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func serviceKeyRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/notifications/send-bulk", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Service-Key", "server-secret")
	return req
}

func signAccess(t *testing.T, key *rsa.PrivateKey, sub string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://api.facinect.local",
		"aud": "facinect-apps",
		"sub": sub,
		"typ": "access",
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
