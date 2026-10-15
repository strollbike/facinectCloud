package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/facinect/booking/internal/authn"
	"github.com/facinect/booking/internal/booking"
	"github.com/facinect/booking/internal/config"
	"github.com/facinect/booking/internal/db"
	"github.com/facinect/booking/internal/httpapi"
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

	svc := &booking.Service{DB: sqlDB, Cfg: cfg}
	startSlotMaintenanceCron(svc, cfg.SlotGenIntervalHours)
	startPastFreeSlotCleanupCron(svc, cfg.PastFreeCleanupMinutes)
	startPaymentHoldExpiryCron(svc)

	srv := &httpapi.Server{Cfg: cfg, Book: svc, JWT: jwtVerifier}
	log.Printf("booking (go) listening on %s service_key=%v jwt=%v slot_gen_hours=%d past_free_cleanup_min=%d tz=%s",
		cfg.HTTPAddr, cfg.ServiceKey != "", jwtVerifier != nil, cfg.SlotGenIntervalHours,
		cfg.PastFreeCleanupMinutes, cfg.SlotTimezone)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// startSlotMaintenanceCron: daily past cleanup + month-window generate;
// on month-end (last day or 1st of month) also wipe previous-month unused slots once.
func startSlotMaintenanceCron(svc *booking.Service, intervalHours int) {
	if intervalHours <= 0 {
		log.Printf("slot maintenance cron disabled (BOOKING_SLOT_GEN_INTERVAL_HOURS=%d)", intervalHours)
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		res, err := svc.RunSlotMaintenance(ctx, time.Now(), false)
		if err != nil {
			log.Printf("slot maintenance cron error: %v", err)
			return
		}
		log.Printf("slot maintenance cron: %s", res.Message)
	}
	go func() {
		time.Sleep(15 * time.Second)
		run()
		t := time.NewTicker(time.Duration(intervalHours) * time.Hour)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}

// startPastFreeSlotCleanupCron deletes only past (and ended today) available slots.
// Booked/blocked are never removed. Lightweight; runs separately from full month maintenance.
func startPastFreeSlotCleanupCron(svc *booking.Service, intervalMinutes int) {
	if intervalMinutes <= 0 {
		log.Printf("past free-slot cleanup cron disabled (BOOKING_PAST_FREE_CLEANUP_MINUTES=%d)", intervalMinutes)
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		n, err := svc.CleanupPastAvailableSlots(ctx)
		if err != nil {
			log.Printf("past free-slot cleanup error: %v", err)
			return
		}
		if n > 0 {
			log.Printf("past free-slot cleanup: deleted %d available slot(s)", n)
		}
	}
	go func() {
		time.Sleep(20 * time.Second)
		run()
		t := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}

// startPaymentHoldExpiryCron frees held slots and cancels payment links ~1m after the 30m TTL.
func startPaymentHoldExpiryCron(svc *booking.Service) {
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := svc.ExpireDuePaymentHolds(ctx)
		if err != nil {
			log.Printf("payment hold expiry error: %v", err)
			return
		}
		if n > 0 {
			log.Printf("payment hold expiry: freed %d slot(s)", n)
		}
	}
	go func() {
		time.Sleep(5 * time.Second)
		run()
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}
