package main

import (
	"log"
	"net/http"

	"github.com/facinect/notifications/internal/authn"
	"github.com/facinect/notifications/internal/config"
	"github.com/facinect/notifications/internal/db"
	"github.com/facinect/notifications/internal/httpapi"
	"github.com/facinect/notifications/internal/notify"
)

func main() {
	cfg := config.FromEnv()
	sqlDB, err := db.Open(cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer sqlDB.Close()
	if err := db.EnsureSchema(sqlDB); err != nil {
		log.Fatalf("schema: %v", err)
	}

	svc := &notify.Service{
		DB:   sqlDB,
		Cfg:  cfg,
		WA:   notify.NewWhatsApp(cfg),
		Push: notify.NewPush(cfg),
	}
	var jwtVerifier *authn.Verifier
	jv, err := authn.NewVerifier(cfg.JWTPublicKeyPath, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		if cfg.AppEnv == "local" || cfg.AppEnv == "development" {
			log.Printf("jwt verifier unavailable (local): %v", err)
		} else {
			log.Fatalf("jwt: %v", err)
		}
	} else {
		jwtVerifier = jv
	}
	srv := &httpapi.Server{Cfg: cfg, Notify: svc, JWT: jwtVerifier}
	log.Printf("notifications (go) listening on %s dry_run=%v whatsapp=%v push=%v",
		cfg.HTTPAddr, cfg.DryRun, cfg.WhatsAppConfigured(), cfg.PushConfigured())
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
