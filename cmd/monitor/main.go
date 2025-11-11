// Package main provides the entry point for the consensus monitoring application.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"consensus-monitoring/internal/collector"
	"consensus-monitoring/internal/config"
	dbpkg "consensus-monitoring/internal/db"
	"consensus-monitoring/internal/logger"
	"consensus-monitoring/internal/tui"

	"github.com/joho/godotenv"
)

func main() {
	// Try to load .env from CWD if present; otherwise use environment as-is
	if _, statErr := os.Stat(".env"); statErr == nil {
		_ = godotenv.Load(".env")
	}

	cfg := config.Load()
	log := logger.New(cfg.Debug)

	fmt.Printf("Consensus monitor starting...\n")
	fmt.Printf("Config loaded: %s\n", cfg.DebugString())
	fmt.Printf("Loading...\n")

	gormDB, err := dbpkg.Open(cfg)
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	if gormDB != nil {
		log.Printf("DB connected")

		if err := dbpkg.AutoMigrate(gormDB); err != nil {
			log.Fatalf("failed to run migrations: %v", err)
		}
		log.Printf("Migrations applied")
	} else {
		log.Printf("DATABASE_URL not provided – persistence disabled")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create channel for TUI updates (only if not in debug mode)
	var tuiUpdateCh chan interface{}
	if !cfg.Debug {
		tuiUpdateCh = make(chan interface{}, collector.TUIChannelBufferSize)
		// Start TUI in a goroutine
		go func() {
			if err := tui.Run(tuiUpdateCh); err != nil {
				log.Printf("TUI error: %v", err)
			}
			// TUI exited, cancel context to trigger shutdown
			cancel()
		}()
	}

	coll, err := collector.NewCollector(cfg, gormDB, tuiUpdateCh, log)
	if err != nil {
		log.Printf("failed to init collector: %v", err)
		return
	}

	go func() {
		if err := coll.Run(ctx); err != nil {
			log.Printf("collector stopped: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	// Close collector first (this will stop all goroutines and connections)
	if err := coll.Close(); err != nil {
		log.Printf("close error: %v", err)
	}

	// Close TUI update channel to stop sending updates
	if tuiUpdateCh != nil {
		close(tuiUpdateCh)
		// Give TUI a moment to process the close and quit
		time.Sleep(collector.TUICloseDelay)
	}

	// Ensure logs flushed in some environments
	_ = os.Stderr.Sync()
	_ = os.Stdout.Sync()
}
