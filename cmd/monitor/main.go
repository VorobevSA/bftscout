package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"consensus-monitoring/internal/collector"
	"consensus-monitoring/internal/config"
	dbpkg "consensus-monitoring/internal/db"

	"github.com/joho/godotenv"
)

func main() {
	log.Printf("Consensus monitor starting")
	// Try to load .env from CWD if present; otherwise use environment as-is
	if _, statErr := os.Stat(".env"); statErr == nil {
		if err := godotenv.Load(".env"); err == nil {
			log.Printf(".env loaded")
		} else {
			log.Printf(".env present but failed to load: %v", err)
		}
	} else {
		wd, _ := os.Getwd()
		log.Printf(".env not found in CWD: %s (using environment)", wd)
	}
	cfg := config.Load()
	log.Printf("Config loaded: %s", cfg.DebugString())
	gormDB, err := dbpkg.Open(cfg)
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	log.Printf("DB connected")

	if err := dbpkg.AutoMigrate(gormDB); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}
	log.Printf("Migrations applied")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	coll, err := collector.NewCollector(cfg, gormDB)
	if err != nil {
		log.Fatalf("failed to init collector: %v", err)
	}

	go func() {
		if err := coll.Run(ctx); err != nil {
			log.Printf("collector stopped: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	if err := coll.Close(); err != nil {
		log.Printf("close error: %v", err)
	}

	// Ensure logs flushed in some environments
	_ = os.Stderr.Sync()
	_ = os.Stdout.Sync()
}
