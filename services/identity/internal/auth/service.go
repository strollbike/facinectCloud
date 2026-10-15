package auth

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials   = errors.New("invalid_credentials")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrEmailNotVerified     = errors.New("email_not_verified")
	ErrInvalidHandoff       = errors.New("invalid_handoff")
	ErrOAuthExpired         = errors.New("oauth_expired")
	ErrInvalidRefresh       = errors.New("invalid_refresh")
	ErrRefreshExpired       = errors.New("refresh_expired")
	ErrOtpNotFound          = errors.New("otp_not_found")
	ErrOtpLocked            = errors.New("otp_locked")
	ErrOtpExpired           = errors.New("otp_expired")
	ErrOtpInvalid           = errors.New("otp_invalid")
	ErrOtpRateLimited       = errors.New("otp_rate_limited")
	ErrNoFacilitiesAssigned = errors.New("no_facilities_assigned")
)

type User struct {
	ID           int64
	Email        sql.NullString
	PhoneE164    sql.NullString
	PasswordHash sql.NullString
	FullName     string
	AvatarURL    sql.NullString
	Status       string
}

type TokenPair struct {
	TokenType    string                 `json:"token_type"`
	AccessToken  string                 `json:"access_token"`
	ExpiresIn    int                    `json:"expires_in"`
	RefreshToken string                 `json:"refresh_token"`
	User         map[string]interface{} `json:"user"`
	Redirect     string                 `json:"redirect,omitempty"`
}

type Service struct {
	DB               *sql.DB
	JWT              *JWTService
	Google           *GoogleOAuth
	AppEnv           string
	NotificationsURL string
	NotificationsKey string
	OTPTemplate      string
	OTPDevInline     bool
	HTTP             *http.Client
}

func (s *Service) BootstrapAdmin(email, password, name string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil
	}
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM users WHERE email = $1 LIMIT 1`, email).Scan(&id)
	hash, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if herr != nil {
		return herr
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = s.DB.QueryRow(
			`INSERT INTO users (email, password_hash, full_name, status) VALUES ($1, $2, $3, 'active') RETURNING id`,
			email, string(hash), name,
		).Scan(&id)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		_, err = s.DB.Exec(
			`UPDATE users SET password_hash = $1, full_name = $2, status = 'active', updated_at = NOW() WHERE id = $3`,
			string(hash), name, id,
		)
		if err != nil {
			return err
		}
	}
	return s.BootstrapDemoFacility(id)
}

func (s *Service) EmailExists(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM users WHERE email = $1 LIMIT 1`, email).Scan(&id)
	return err == nil
}

func (s *Service) LoginWithPassword(email, password, ip string) (*TokenPair, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	user, err := s.findUserByEmail(email)
	if err != nil || !user.PasswordHash.Valid {
		_ = s.audit(nil, "login_failed", ip, map[string]interface{}{"email": email})
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash.String), []byte(password)) != nil {
		_ = s.audit(nil, "login_failed", ip, map[string]interface{}{"email": email})
		return nil, ErrInvalidCredentials
	}
	_ = s.audit(&user.ID, "login_password", ip, nil)
	return s.issuePartnerTokenPair(user)
}

func (s *Service) LoginWithGoogle(info GoogleUserInfo, ip string) (*TokenPair, error) {
	email := strings.ToLower(strings.TrimSpace(info.Email))
	sub := strings.TrimSpace(info.Sub)
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = email
	}
	if email == "" || !info.EmailVerified {
		return nil, ErrEmailNotVerified
	}

	var user *User
	if sub != "" {
		user, _ = s.findUserByGoogleSub(sub)
	}
	if user == nil {
		user, _ = s.findUserByEmail(email)
	}
	if user == nil {
		var id int64
		err := s.DB.QueryRow(
			`INSERT INTO users (email, full_name, avatar_url, status) VALUES ($1, $2, $3, 'active') RETURNING id`,
			email, name, nullStr(info.Picture),
		).Scan(&id)
		if err != nil {
			return nil, err
		}
		user, err = s.UserByID(id)
		if err != nil {
			return nil, err
		}
	} else {
		_, _ = s.DB.Exec(
			`UPDATE users SET full_name = COALESCE(NULLIF($1, ''), full_name),
			 avatar_url = COALESCE(NULLIF($2, ''), avatar_url),
			 updated_at = NOW()
			 WHERE id = $3`,
			name, strings.TrimSpace(info.Picture), user.ID,
		)
		user, _ = s.UserByID(user.ID)
	}
	if user.Status != "active" {
		return nil, ErrUnauthorized
	}
	if sub != "" {
		_, _ = s.DB.Exec(
			`INSERT INTO oauth_accounts (user_id, provider, provider_sub, email) VALUES ($1, 'google', $2, $3)
			 ON CONFLICT (provider, provider_sub) DO NOTHING`,
			user.ID, sub, email,
		)
	}
	_ = s.audit(&user.ID, "login_google", ip, map[string]interface{}{"email": email})
	return s.issuePartnerTokenPair(user)
}

func (s *Service) SavePKCE(state, verifier, redirectURI string) error {
	expires := time.Now().UTC().Add(10 * time.Minute)
	_, err := s.DB.Exec(
		`INSERT INTO oauth_pkce (state, code_verifier, redirect_uri, expires_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (state) DO UPDATE SET
		   code_verifier = EXCLUDED.code_verifier,
		   redirect_uri = EXCLUDED.redirect_uri,
		   expires_at = EXCLUDED.expires_at`,
		state, verifier, redirectURI, expires,
	)
	return err
}

func (s *Service) ConsumePKCE(state string) (verifier, redirectURI string, ok bool) {
	var exp time.Time
	err := s.DB.QueryRow(
		`SELECT code_verifier, redirect_uri, expires_at FROM oauth_pkce WHERE state = $1 LIMIT 1`, state,
	).Scan(&verifier, &redirectURI, &exp)
	_, _ = s.DB.Exec(`DELETE FROM oauth_pkce WHERE state = $1`, state)
	if err != nil || time.Now().UTC().After(exp) {
		return "", "", false
	}
	return verifier, redirectURI, true
}

func (s *Service) CreateHandoff(pair *TokenPair) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b)
	payload, err := json.Marshal(pair)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(2 * time.Minute)
	_, err = s.DB.Exec(
		`INSERT INTO oauth_handoff (code_hash, payload_json, expires_at) VALUES ($1, $2::jsonb, $3)`,
		HashToken(code), payload, expires,
	)
	return code, err
}

func (s *Service) ConsumeHandoff(code string) (*TokenPair, error) {
	hash := HashToken(code)
	var payload []byte
	var exp time.Time
	err := s.DB.QueryRow(
		`SELECT payload_json, expires_at FROM oauth_handoff WHERE code_hash = $1 AND consumed_at IS NULL LIMIT 1`,
		hash,
	).Scan(&payload, &exp)
	if err != nil {
		return nil, ErrInvalidHandoff
	}
	if time.Now().UTC().After(exp) {
		return nil, ErrOAuthExpired
	}
	_, _ = s.DB.Exec(`UPDATE oauth_handoff SET consumed_at = NOW() WHERE code_hash = $1`, hash)
	var pair TokenPair
	if err := json.Unmarshal(payload, &pair); err != nil {
		return nil, ErrInvalidHandoff
	}
	return &pair, nil
}

func (s *Service) RequestOTP(phone string) (map[string]interface{}, error) {
	phone = normalizePhone(phone)
	if phone == "" {
		return nil, fmt.Errorf("phone_required")
	}
	code := fmt.Sprintf("%06d", randInt(100000, 999999))
	hash := HashToken(phone + ":" + code)
	expires := time.Now().UTC().Add(5 * time.Minute)
	_, err := s.DB.Exec(
		`INSERT INTO otp_challenges (phone_e164, code_hash, expires_at) VALUES ($1, $2, $3)`,
		phone, hash, expires,
	)
	if err != nil {
		return nil, err
	}

	out := map[string]interface{}{
		"ok":         true,
		"phone":      phone,
		"expires_in": 300,
		"delivery":   "whatsapp",
	}

	if s.OTPDevInline {
		out["delivery"] = "dev_inline"
		out["dev_otp"] = code
		return out, nil
	}

	if err := s.dispatchOTPWhatsApp(phone, code); err != nil {
		// Local fallback so engineers can still verify when Meta is down.
		if s.AppEnv == "local" || s.AppEnv == "development" {
			out["delivery"] = "dev_inline"
			out["dev_otp"] = code
			out["whatsapp_error"] = err.Error()
			return out, nil
		}
		return nil, fmt.Errorf("whatsapp_send_failed: %w", err)
	}
	return out, nil
}

func (s *Service) dispatchOTPWhatsApp(phone, code string) error {
	base := strings.TrimRight(strings.TrimSpace(s.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	tmpl := strings.TrimSpace(s.OTPTemplate)
	if tmpl == "" {
		tmpl = "facinect_otp"
	}
	payload := map[string]interface{}{
		"channel":  "whatsapp",
		"template": tmpl,
		"to": map[string]string{
			"whatsapp": phone,
		},
		"data": map[string]interface{}{
			"otp":  code,
			"code": code,
		},
		"priority": "high",
	}
	raw, _ := json.Marshal(payload)
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.NotificationsKey)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var job map[string]interface{}
	_ = json.Unmarshal(body, &job)
	status := strings.ToLower(fmt.Sprint(job["status"]))
	if status == "failed" {
		errText := strings.TrimSpace(fmt.Sprint(job["error_text"]))
		if errText == "" {
			errText = "send_failed"
		}
		return fmt.Errorf("%s", errText)
	}
	if status == "dry_run" {
		return fmt.Errorf("whatsapp_dry_run")
	}
	return nil
}

func (s *Service) VerifyOTP(phone, code, ip string) (*TokenPair, error) {
	phone = normalizePhone(phone)
	code = digitsOnly(code)
	if phone == "" || len(code) < 4 {
		return nil, fmt.Errorf("phone_and_code_required")
	}
	var id int64
	var hash string
	var attempts int
	var exp time.Time
	err := s.DB.QueryRow(
		`SELECT id, code_hash, attempts, expires_at FROM otp_challenges
		 WHERE phone_e164 = $1 AND consumed_at IS NULL ORDER BY id DESC LIMIT 1`, phone,
	).Scan(&id, &hash, &attempts, &exp)
	if err != nil {
		return nil, ErrOtpNotFound
	}
	if attempts >= 5 {
		return nil, ErrOtpLocked
	}
	if time.Now().UTC().After(exp) {
		return nil, ErrOtpExpired
	}
	if HashToken(phone+":"+code) != hash {
		_, _ = s.DB.Exec(`UPDATE otp_challenges SET attempts = attempts + 1 WHERE id = $1`, id)
		return nil, ErrOtpInvalid
	}
	_, _ = s.DB.Exec(`UPDATE otp_challenges SET consumed_at = NOW() WHERE id = $1`, id)
	user, err := s.findOrCreateByPhone(phone)
	if err != nil {
		return nil, err
	}
	_ = s.audit(&user.ID, "login_otp", ip, map[string]interface{}{"phone": phone})
	return s.issueTokenPair(user)
}

func (s *Service) Refresh(refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token_required")
	}
	hash := HashToken(refreshToken)
	var tokenID, userID int64
	var exp time.Time
	err := s.DB.QueryRow(
		`SELECT rt.id, rt.user_id, rt.expires_at
		 FROM refresh_tokens rt INNER JOIN users u ON u.id = rt.user_id
		 WHERE rt.token_hash = $1 AND rt.revoked_at IS NULL AND u.status = 'active' LIMIT 1`, hash,
	).Scan(&tokenID, &userID, &exp)
	if err != nil {
		return nil, ErrInvalidRefresh
	}
	if time.Now().UTC().After(exp) {
		return nil, ErrRefreshExpired
	}
	_, _ = s.DB.Exec(`UPDATE refresh_tokens SET revoked_at = NOW() WHERE id = $1`, tokenID)
	user, err := s.UserByID(userID)
	if err != nil {
		return nil, ErrInvalidRefresh
	}
	return s.issueTokenPair(user)
}

func (s *Service) RevokeRefresh(refreshToken string) {
	if refreshToken == "" {
		return
	}
	_, _ = s.DB.Exec(
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE token_hash = $1 AND revoked_at IS NULL`,
		HashToken(refreshToken),
	)
}

func (s *Service) UserByID(id int64) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(
		`SELECT id, email, phone_e164, password_hash, full_name, avatar_url, status FROM users WHERE id = $1 LIMIT 1`, id,
	).Scan(&u.ID, &u.Email, &u.PhoneE164, &u.PasswordHash, &u.FullName, &u.AvatarURL, &u.Status)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) PublicUser(u *User) map[string]interface{} {
	var email, phone, avatar interface{}
	if u.Email.Valid {
		email = u.Email.String
	}
	if u.PhoneE164.Valid {
		phone = u.PhoneE164.String
	}
	if u.AvatarURL.Valid && u.AvatarURL.String != "" {
		avatar = u.AvatarURL.String
	}
	return map[string]interface{}{
		"id":         u.ID,
		"email":      email,
		"phone":      phone,
		"full_name":  u.FullName,
		"avatar_url": avatar,
		"status":     u.Status,
		"has_password": u.PasswordHash.Valid && u.PasswordHash.String != "",
	}
}

func (s *Service) SessionPayload(u *User) (map[string]interface{}, []FacilityMembership, string, error) {
	memberships, err := s.LoadMemberships(u.ID)
	if err != nil {
		return nil, nil, "", err
	}
	out := s.PublicUser(u)
	facPublic := make([]map[string]interface{}, 0, len(memberships))
	ids := make([]int64, 0, len(memberships))
	for _, m := range memberships {
		facPublic = append(facPublic, m.Public())
		ids = append(ids, m.FacilityID)
	}
	out["facilities"] = facPublic
	out["facility_ids"] = ids
	redirect := ""
	if len(memberships) > 0 {
		first := memberships[0]
		out["role"] = first.Role
		out["page_access"] = first.PageAccess
		out["current_facility_id"] = first.FacilityID
		redirect = FacilityHomePath(first)
		out["redirect"] = redirect
	} else {
		out["role"] = ""
		out["page_access"] = []string{}
		out["current_facility_id"] = nil
	}
	return out, memberships, redirect, nil
}

func (s *Service) issuePartnerTokenPair(u *User) (*TokenPair, error) {
	userOut, memberships, redirect, err := s.SessionPayload(u)
	if err != nil {
		return nil, err
	}
	if len(memberships) == 0 {
		return nil, ErrNoFacilitiesAssigned
	}
	pair, err := s.issueTokenPairWithSession(u, userOut, memberships)
	if err != nil {
		return nil, err
	}
	pair.Redirect = redirect
	return pair, nil
}

func (s *Service) issueTokenPair(u *User) (*TokenPair, error) {
	userOut, memberships, redirect, err := s.SessionPayload(u)
	if err != nil {
		return nil, err
	}
	pair, err := s.issueTokenPairWithSession(u, userOut, memberships)
	if err != nil {
		return nil, err
	}
	pair.Redirect = redirect
	return pair, nil
}

func (s *Service) issueTokenPairWithSession(u *User, userOut map[string]interface{}, memberships []FacilityMembership) (*TokenPair, error) {
	email, phone := "", ""
	if u.Email.Valid {
		email = u.Email.String
	}
	if u.PhoneE164.Valid {
		phone = u.PhoneE164.String
	}
	role := ""
	pageAccess := []string{}
	facilityIDs := make([]int64, 0, len(memberships))
	if len(memberships) > 0 {
		role = memberships[0].Role
		pageAccess = memberships[0].PageAccess
		for _, m := range memberships {
			facilityIDs = append(facilityIDs, m.FacilityID)
		}
	}
	access, err := s.JWT.IssueAccess(AccessClaims{
		UserID:      u.ID,
		Name:        u.FullName,
		Email:       email,
		Phone:       phone,
		Role:        role,
		PageAccess:  pageAccess,
		FacilityIDs: facilityIDs,
	})
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	refresh := hex.EncodeToString(raw)
	jti := uuid.NewString()
	expires := time.Now().UTC().Add(s.JWT.RefreshTTL())
	_, err = s.DB.Exec(
		`INSERT INTO refresh_tokens (user_id, jti, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		u.ID, jti, HashToken(refresh), expires,
	)
	if err != nil {
		return nil, err
	}
	return &TokenPair{
		TokenType:    "Bearer",
		AccessToken:  access,
		ExpiresIn:    int(s.JWT.AccessTTL().Seconds()),
		RefreshToken: refresh,
		User:         userOut,
	}, nil
}

func (s *Service) findUserByEmail(email string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(
		`SELECT id, email, phone_e164, password_hash, full_name, avatar_url, status FROM users WHERE email = $1 AND status = 'active' LIMIT 1`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&u.ID, &u.Email, &u.PhoneE164, &u.PasswordHash, &u.FullName, &u.AvatarURL, &u.Status)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) findUserByGoogleSub(sub string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(
		`SELECT u.id, u.email, u.phone_e164, u.password_hash, u.full_name, u.avatar_url, u.status
		 FROM oauth_accounts o INNER JOIN users u ON u.id = o.user_id
		 WHERE o.provider = 'google' AND o.provider_sub = $1 LIMIT 1`, sub,
	).Scan(&u.ID, &u.Email, &u.PhoneE164, &u.PasswordHash, &u.FullName, &u.AvatarURL, &u.Status)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) findOrCreateByPhone(phone string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(
		`SELECT id, email, phone_e164, password_hash, full_name, avatar_url, status FROM users WHERE phone_e164 = $1 LIMIT 1`, phone,
	).Scan(&u.ID, &u.Email, &u.PhoneE164, &u.PasswordHash, &u.FullName, &u.AvatarURL, &u.Status)
	if err == nil {
		return u, nil
	}
	name := "User " + phone[max(0, len(phone)-4):]
	var id int64
	err = s.DB.QueryRow(
		`INSERT INTO users (phone_e164, full_name, status) VALUES ($1, $2, 'active') RETURNING id`,
		phone, name,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.UserByID(id)
}

func (s *Service) SetPassword(userID int64, currentPassword, newPassword string) error {
	newPassword = strings.TrimSpace(newPassword)
	if len(newPassword) < 8 {
		return fmt.Errorf("password_too_short")
	}
	user, err := s.UserByID(userID)
	if err != nil {
		return ErrUnauthorized
	}
	// Caller is already JWT-authenticated (partner settings). Current password is
	// optional: if provided and a hash exists, verify it; otherwise allow set/update.
	if user.PasswordHash.Valid && user.PasswordHash.String != "" {
		currentPassword = strings.TrimSpace(currentPassword)
		if currentPassword != "" {
			if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash.String), []byte(currentPassword)) != nil {
				return fmt.Errorf("invalid_current_password")
			}
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(
		`UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		string(hash), userID,
	)
	return err
}

func nullStr(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) audit(userID *int64, event, ip string, meta map[string]interface{}) error {
	var metaArg interface{}
	if meta != nil {
		b, _ := json.Marshal(meta)
		metaArg = b
	}
	var uid interface{}
	if userID != nil {
		uid = *userID
	}
	var ipVal interface{}
	if ip != "" {
		ipVal = ip
	}
	_, err := s.DB.Exec(
		`INSERT INTO audit_log (user_id, event, ip, meta_json) VALUES ($1, $2, $3, $4)`,
		uid, event, ipVal, metaArg,
	)
	return err
}

func normalizePhone(phone string) string {
	d := digitsOnly(phone)
	if len(d) == 10 {
		d = "91" + d
	}
	return d
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func randInt(min, max int) int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if n < 0 {
		n = -n
	}
	return min + n%(max-min+1)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
