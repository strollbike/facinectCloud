package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv            string
	HTTPAddr          string
	DBHost            string
	DBPort            string
	DBUser            string
	DBPassword        string
	DBName            string
	ServiceKey        string
	CORSOrigins       string
	JWTPublicKeyPath  string
	JWTIssuer         string
	JWTAudience       string
	DefaultOpenHour   int
	DefaultCloseHour  int
	DefaultSlotMins   int
	// SlotGenIntervalHours: background top-up of PHP month window (0 = disabled).
	SlotGenIntervalHours int
	// SlotTimezone: calendar day for past free-slot cleanup (e.g. Asia/Kolkata).
	SlotTimezone string
	// PastFreeCleanupMinutes: how often to delete past available slots only (0 = disabled).
	PastFreeCleanupMinutes int
	// PaygatewayURL: create Razorpay/stub payment links for booking holds.
	PaygatewayURL        string
	PaygatewayServiceKey string
}

func FromEnv() Config {
	return Config{
		AppEnv:                   getenv("APP_ENV", "local"),
		HTTPAddr:                 getenv("HTTP_ADDR", ":8080"),
		DBHost:                   getenv("DB_HOST", "postgres"),
		DBPort:                   getenv("DB_PORT", "5432"),
		DBUser:                   getenv("DB_USER", "identity"),
		DBPassword:               os.Getenv("DB_PASSWORD"),
		DBName:                   getenv("DB_NAME", "fac_identity"),
		ServiceKey:               strings.TrimSpace(os.Getenv("BOOKING_SERVICE_KEY")),
		CORSOrigins:              getenv("CORS_ORIGINS", "*"),
		JWTPublicKeyPath:         getenv("JWT_PUBLIC_KEY_PATH", "/keys/jwt_public.pem"),
		JWTIssuer:                getenv("JWT_ISSUER", "https://api.facinect.local"),
		JWTAudience:              getenv("JWT_AUDIENCE", "facinect-apps"),
		DefaultOpenHour:          getenvInt("BOOKING_OPEN_HOUR", 6),
		DefaultCloseHour:         getenvInt("BOOKING_CLOSE_HOUR", 22),
		DefaultSlotMins:          getenvInt("BOOKING_SLOT_MINUTES", 60),
		SlotGenIntervalHours:     getenvInt("BOOKING_SLOT_GEN_INTERVAL_HOURS", 24),
		SlotTimezone:             getenv("BOOKING_SLOT_TIMEZONE", "Asia/Kolkata"),
		PastFreeCleanupMinutes:   getenvInt("BOOKING_PAST_FREE_CLEANUP_MINUTES", 60),
		PaygatewayURL:            strings.TrimRight(getenv("PAYGATEWAY_URL", "http://paygateway:8080"), "/"),
		PaygatewayServiceKey:     strings.TrimSpace(os.Getenv("PAYGATEWAY_SERVICE_KEY")),
	}
}

func (c Config) IsLocal() bool {
	return c.AppEnv == "local" || c.AppEnv == "development"
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
