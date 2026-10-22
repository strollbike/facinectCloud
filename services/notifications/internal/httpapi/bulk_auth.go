package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// staffMayNotify is the facility role allowed to send class-over and other
// app broadcasts. The service key path does not use this list.
func staffMayNotify(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "sub_admin", "subadmin", "coach", "headcoach", "head_coach":
		return true
	default:
		return false
	}
}

func (s *Server) withSendBulkAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authorized(r) {
			next(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		status, code := s.staffBulkStatus(r, body)
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": code})
			return
		}
		next(w, r)
	}
}

// staffBulkStatus returns 0 when the logged-in facility staff may send.
func (s *Server) staffBulkStatus(r *http.Request, body []byte) (int, string) {
	if s.JWT == nil {
		return http.StatusUnauthorized, "unauthorized"
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authz) < 8 || !strings.EqualFold(authz[:7], "bearer ") {
		return http.StatusUnauthorized, "unauthorized"
	}
	p, err := s.JWT.ParseAccess(strings.TrimSpace(authz[7:]))
	if err != nil || p == nil || p.UserID <= 0 {
		return http.StatusUnauthorized, "unauthorized"
	}
	var payload struct {
		FacilityID *int64 `json:"facility_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.FacilityID == nil || *payload.FacilityID <= 0 {
		return http.StatusBadRequest, "facility_id_required"
	}
	role, err := s.lookupStaffRole(r.Context(), p.UserID, *payload.FacilityID)
	if err != nil {
		return http.StatusInternalServerError, "role_lookup_failed"
	}
	if !staffMayNotify(role) {
		return http.StatusForbidden, "forbidden_facility"
	}
	return 0, ""
}

func (s *Server) lookupStaffRole(ctx context.Context, userID, facilityID int64) (string, error) {
	if s.lookupRole != nil {
		return s.lookupRole(ctx, userID, facilityID)
	}
	if s.Notify == nil || s.Notify.DB == nil {
		return "", sql.ErrConnDone
	}
	var role string
	err := s.Notify.DB.QueryRowContext(ctx, `
SELECT role FROM user_facility_memberships
WHERE user_id=$1 AND facility_id=$2 AND status='active'
LIMIT 1`, userID, facilityID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}
