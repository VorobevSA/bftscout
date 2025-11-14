// Package main provides the entry point for the consensus monitoring application.
package main

import (
	"consensus-monitoring/internal/collector"
	"consensus-monitoring/internal/config"
	"consensus-monitoring/internal/logger"
	"consensus-monitoring/internal/tui"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	dbpkg "consensus-monitoring/internal/db"

	"github.com/joho/godotenv"
)

func main() {
	// Try to load .env from CWD if present; otherwise use environment as-is
	if _, statErr := os.Stat(".env"); statErr == nil {
		_ = godotenv.Load(".env")
	}

	cfg := config.Load()
	
	// If debug logs are enabled, write them to file to avoid interfering with TUI
	var logWriter io.Writer = os.Stderr
	if cfg.Debug {
		logFile, err := os.OpenFile("monitor.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logWriter = logFile
			fmt.Fprintf(os.Stderr, "Debug logs written to monitor.log\n")
		} else {
			fmt.Fprintf(os.Stderr, "Warning: failed to open log file, logs will go to stderr (may interfere with TUI): %v\n", err)
		}
	}
	
	log := logger.NewWithWriter(cfg.Debug, logWriter)

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

	// Create channel for TUI updates (TUI is always enabled)
	tuiUpdateCh := make(chan interface{}, collector.TUIChannelBufferSize)
	// Start TUI in a goroutine
	go func() {
		if err := tui.Run(tuiUpdateCh); err != nil {
			log.Printf("TUI error: %v", err)
		}
		// TUI exited, cancel context to trigger shutdown
		cancel()
	}()

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
	close(tuiUpdateCh)
	// Give TUI a moment to process the close and quit
	time.Sleep(collector.TUICloseDelay)

	// Ensure logs flushed in some environments
	_ = os.Stderr.Sync()
	_ = os.Stdout.Sync()
}
