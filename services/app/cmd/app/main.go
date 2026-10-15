package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/facinect/app/internal/app"
	"github.com/facinect/app/internal/authn"
	"github.com/facinect/app/internal/config"
	"github.com/facinect/app/internal/db"
	"github.com/facinect/app/internal/httpapi"
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

	var jwtVerifier *authn.Verifier
	jv, err := authn.NewVerifier(cfg.JWTPublicKeyPath, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		if cfg.IsLocal() {
			log.Printf("jwt verifier unavailable (local): %v", err)
		} else {
			log.Fatalf("jwt: %v", err)
		}
	} else {
		jwtVerifier = jv
	}

	svc := &app.Service{DB: sqlDB, Cfg: cfg}
	if err := svc.Seed(sqlDB); err != nil {
		log.Printf("seed warn: %v", err)
	}
	startNotifPurgeCron(svc)

	srv := &httpapi.Server{Cfg: cfg, App: svc, JWT: jwtVerifier}
	log.Printf("app consumer (go) listening on %s jwt=%v", cfg.HTTPAddr, jwtVerifier != nil)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// Hard-delete soft-deleted inbox rows after 90 days (daily).
func startNotifPurgeCron(svc *app.Service) {
	go func() {
		time.Sleep(25 * time.Second)
		run := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			n, err := svc.PurgeSoftDeletedNotifications(ctx, 90)
			if err != nil {
				log.Printf("notif purge error: %v", err)
				return
			}
			if n > 0 {
				log.Printf("notif purge: hard-deleted %d soft-deleted row(s) older than 90 days", n)
			}
		}
		run()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}
