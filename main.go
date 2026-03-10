package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/pkg/logger"
	"github.com/3ggie-AB/Sailbit-backend-pos/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic("failed to load config: " + err.Error())
	}

	log := logger.New(cfg.App.Env)
	defer log.Sync() //nolint:errcheck

	srv, err := server.New(cfg, log)
	if err != nil {
		log.Fatal("failed to create server", logger.Err(err))
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Info("server starting", logger.String("addr", cfg.App.Addr))
		if err := srv.Start(); err != nil {
			log.Error("server error", logger.Err(err))
		}
	}()

	<-quit
	log.Info("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown error", logger.Err(err))
	}

	log.Info("server stopped")
}