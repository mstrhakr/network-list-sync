package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	appLog "github.com/mstrhakr/network-list-sync/internal/logging"

	"github.com/mstrhakr/network-list-sync/internal/scheduler"
	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/syncer"
	"github.com/mstrhakr/network-list-sync/internal/web"
)

//go:embed ui
var uiContent embed.FS

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "sync.db", "SQLite database path")
	debug := flag.Bool("debug", false, "Enable debug logs")
	verbose := flag.Bool("verbose", false, "Enable verbose logs")
	logFile := flag.String("log-file", "sync.log", "Log file path (empty disables file logging)")
	showVersion := flag.Bool("version", false, "Print version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("network-list-sync version=%s commit=%s built=%s\n", version, commit, buildDate)
		return
	}

	logCfg := appLog.DefaultConfig()
	logCfg.DebugEnabled = *debug
	logCfg.VerboseEnabled = *verbose
	if *logFile == "" {
		logCfg.LogToFile = false
	} else {
		logCfg.LogToFile = true
		logCfg.FilePath = *logFile
	}

	mgr := appLog.GetManager()
	if err := mgr.Configure(logCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to configure logging: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	slog.Info("Logging configured",
		"log_to_stdout", logCfg.LogToStdout,
		"log_to_file", logCfg.LogToFile,
		"log_file", logCfg.FilePath,
		"debug", logCfg.DebugEnabled,
		"verbose", logCfg.VerboseEnabled,
	)

	db, err := store.New(*dbPath)
	if err != nil {
		slog.Error("Failed to open database", "path", *dbPath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	syn := syncer.New()

	sched := scheduler.New(db, syn)
	if err := sched.Start(); err != nil {
		slog.Error("Failed to start scheduler", "error", err)
		os.Exit(1)
	}
	defer sched.Stop()

	uiFS, err := fs.Sub(uiContent, "ui")
	if err != nil {
		slog.Error("Failed to load embedded UI", "error", err)
		os.Exit(1)
	}

	handler := web.NewHandler(db, syn, sched, uiFS)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Server starting", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("Shutdown signal received", "signal", sig.String())
	case err := <-serverErr:
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Shutdown error", "error", err)
		return
	}
	slog.Info("Shutdown complete")
}
