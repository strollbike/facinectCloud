package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// phoneLast10SQL returns a Postgres expression: last 10 digits of a phone column.
func phoneLast10SQL(columnExpr string) string {
	return fmt.Sprintf(
		"RIGHT(regexp_replace(COALESCE(%s,''), '\\D', '', 'g'), 10)",
		columnExpr,
	)
}

func last10Digits(normalized string) string {
	d := digitsOnly(normalized)
	if len(d) >= 10 {
		return d[len(d)-10:]
	}
	return d
}

// resolvePortalWhatsApp mirrors PHP sp_resolve_whatsapp / pm_resolve_whatsapp.
func (s *Service) resolvePortalWhatsApp(ctx context.Context, body map[string]any) string {
	raw := strField(body, "whatsappNo")
	if raw == "" {
		raw = strField(body, "whatsappNumber")
	}
	if raw == "" {
		raw = strField(body, "whatsapp")
	}
	if raw == "" {
		uid, _ := parseInt64Field(body, "userId")
		if uid > 0 {
			row, _ := s.userRow(ctx, uid, "")
			if row != nil {
				if v := fmt.Sprint(row["whatsappNo"]); v != "" && v != "<nil>" {
					raw = v
				} else if v := fmt.Sprint(row["phone"]); v != "" && v != "<nil>" {
					raw = v
				}
			}
		}
	}
	return normalizeWA(raw)
}

func parseInt64Field(body map[string]any, key string) (int64, bool) {
	v, ok := body[key]
	if !ok || v == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
	return n, err == nil && n > 0
}

// resolvePortalLast10 returns last 10 digits or an error message for the client.
func (s *Service) resolvePortalLast10(ctx context.Context, body map[string]any) (string, string) {
	wa := s.resolvePortalWhatsApp(ctx, body)
	last10 := last10Digits(wa)
	if len(last10) < 10 {
		return "", "whatsappNo or userId is required."
	}
	return last10, ""
}

func (s *Service) tableExists(ctx context.Context, name string) bool {
	var one int
	err := s.DB.QueryRowContext(ctx, `
SELECT 1 FROM information_schema.tables
WHERE table_schema = 'public' AND table_name = $1
LIMIT 1`, name).Scan(&one)
	return err == nil
}
