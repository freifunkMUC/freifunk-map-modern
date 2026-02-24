package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/freifunkMUC/freifunk-map-modern/internal/api"
	"github.com/freifunkMUC/freifunk-map-modern/internal/config"
	"github.com/freifunkMUC/freifunk-map-modern/internal/federation"
	"github.com/freifunkMUC/freifunk-map-modern/internal/sse"
	"github.com/freifunkMUC/freifunk-map-modern/internal/store"
)

//go:embed web/*
var webFS embed.FS

func main() {
	cfgPath := "config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	hub := sse.NewHub()
	var s *store.Store
	var fedStore *federation.Store

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.Federation {
		fedStore = federation.NewStore(cfg)
		s = fedStore.Store

		// Try to restore cached state for instant startup
		if fedStore.RestoreState() {
			log.Println("Federation mode: serving cached data, refreshing in background...")
			go func() {
				old := fedStore.GetSnapshot()
				if err := fedStore.DiscoverAndRefresh(); err != nil {
					log.Printf("Warning: background federation refresh failed: %v", err)
					return
				}
				snap := fedStore.GetSnapshot()
				log.Printf("Background refresh complete: %d nodes (%d online)",
					snap.Stats.TotalNodes, snap.Stats.OnlineNodes)
				diff := store.ComputeDiff(old, snap)
				if diff != nil {
					hub.Broadcast(diff)
				}
			}()
		} else {
			log.Println("Federation mode: no cache, performing initial discovery...")
			if err := fedStore.DiscoverAndRefresh(); err != nil {
				log.Printf("Warning: initial federation discovery failed: %v", err)
			}
		}
		go fedStore.RunRefreshLoop(ctx, hub)
	} else {
		s = store.New(cfg)
		if err := s.Refresh(); err != nil {
			log.Printf("Warning: initial data fetch failed: %v", err)
		}
		go s.RunRefreshLoop(ctx, hub)
	}

	mux := http.NewServeMux()
	api.RegisterHandlers(mux, cfg, s, hub)

	if fedStore != nil {
		api.RegisterFederationHandlers(mux, cfg, fedStore)
	} else {
		api.RegisterMetricsHandler(mux, cfg)
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("Failed to mount web FS: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webContent)))

	server := &http.Server{
		Addr:         cfg.Listen,
		Handler:      api.GzipHandler(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("🗺️  Freifunk Map starting on %s", cfg.Listen)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
}
