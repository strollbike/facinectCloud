package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv             string
	HTTPAddr           string
	DBHost             string
	DBPort             string
	DBUser             string
	DBPassword         string
	DBName             string
	RedisHost          string
	RedisPort          string
	ServiceKey         string
	CORSOrigins        string
	JWTPublicKeyPath   string
	JWTIssuer          string
	JWTAudience        string
	MetaWhatsAppToken      string
	MetaPhoneNumberID      string
	MetaAPIVersion         string
	FCMServerKey           string
	FCMServiceAccountJSON  string
	FCMProjectID           string
	DryRun                 bool
}

func FromEnv() Config {
	dry := strings.EqualFold(os.Getenv("NOTIFICATIONS_DRY_RUN"), "true") ||
		strings.EqualFold(os.Getenv("NOTIFICATIONS_DRY_RUN"), "1")
	if os.Getenv("NOTIFICATIONS_DRY_RUN") == "" {
		// Default dry-run in local when Meta token missing
		dry = strings.TrimSpace(os.Getenv("META_WHATSAPP_TOKEN")) == ""
	}
	return Config{
		AppEnv:            getenv("APP_ENV", "local"),
		HTTPAddr:          getenv("HTTP_ADDR", ":8080"),
		DBHost:            getenv("DB_HOST", "postgres"),
		DBPort:            getenv("DB_PORT", "5432"),
		DBUser:            getenv("DB_USER", "identity"),
		DBPassword:        os.Getenv("DB_PASSWORD"),
		DBName:            getenv("DB_NAME", "fac_identity"),
		RedisHost:         getenv("REDIS_HOST", "redis"),
		RedisPort:         getenv("REDIS_PORT", "6379"),
		ServiceKey:        strings.TrimSpace(os.Getenv("NOTIFICATIONS_SERVICE_KEY")),
		CORSOrigins:       getenv("CORS_ORIGINS", "*"),
		JWTPublicKeyPath:  getenv("JWT_PUBLIC_KEY_PATH", "/keys/jwt_public.pem"),
		JWTIssuer:         getenv("JWT_ISSUER", "https://api.facinect.local"),
		JWTAudience:       getenv("JWT_AUDIENCE", "facinect-apps"),
		MetaWhatsAppToken:     strings.TrimSpace(os.Getenv("META_WHATSAPP_TOKEN")),
		MetaPhoneNumberID:     strings.TrimSpace(os.Getenv("META_PHONE_NUMBER_ID")),
		MetaAPIVersion:        getenv("META_API_VERSION", "v21.0"),
		FCMServerKey:          strings.TrimSpace(os.Getenv("FCM_SERVER_KEY")),
		FCMServiceAccountJSON: strings.TrimSpace(os.Getenv("FCM_SERVICE_ACCOUNT_JSON")),
		FCMProjectID:          strings.TrimSpace(os.Getenv("FCM_PROJECT_ID")),
		DryRun:                dry,
	}
}

func (c Config) WhatsAppConfigured() bool {
	return c.MetaWhatsAppToken != "" && c.MetaPhoneNumberID != ""
}

func (c Config) PushConfigured() bool {
	if c.FCMServiceAccountJSON != "" {
		if st, err := os.Stat(c.FCMServiceAccountJSON); err == nil && !st.IsDir() {
			return true
		}
	}
	return c.FCMServerKey != ""
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
