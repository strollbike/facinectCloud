package authn

import (
	"fmt"
	"os"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
)

type Verifier struct {
	publicKey interface{}
	issuer    string
	audience  string
}

type Principal struct {
	UserID      int64
	Email       string
	Role        string
	FacilityIDs []int64
}

func NewVerifier(pubPath, issuer, audience string) (*Verifier, error) {
	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	pub, err := jwt.ParseRSAPublicKeyFromPEM(pubPEM)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return &Verifier{publicKey: pub, issuer: issuer, audience: audience}, nil
}

func (v *Verifier) ParseAccess(tokenStr string) (*Principal, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected_signing_method")
		}
		return v.publicKey, nil
	}, jwt.WithAudience(v.audience), jwt.WithIssuer(v.issuer))
	if err != nil {
		return nil, fmt.Errorf("invalid_token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid_token")
	}
	if typ, _ := claims["typ"].(string); typ != "" && typ != "access" {
		return nil, fmt.Errorf("not_access_token")
	}
	sub, _ := claims["sub"].(string)
	uid, _ := strconv.ParseInt(sub, 10, 64)
	if uid <= 0 {
		return nil, fmt.Errorf("invalid_subject")
	}
	p := &Principal{
		UserID: uid,
		Email:  stringClaim(claims, "email"),
		Role:   stringClaim(claims, "role"),
	}
	if raw, ok := claims["facilities"]; ok {
		p.FacilityIDs = int64Slice(raw)
	}
	return p, nil
}

func stringClaim(c jwt.MapClaims, key string) string {
	v, _ := c[key].(string)
	return v
}

func int64Slice(raw interface{}) []int64 {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, v := range arr {
		switch n := v.(type) {
		case float64:
			out = append(out, int64(n))
		case int64:
			out = append(out, n)
		}
	}
	return out
}
