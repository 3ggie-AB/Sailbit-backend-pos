package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"go.uber.org/zap"
)

func main() {
	// ── Config ────────────────────────────────────────────────────────
	cfg, err := loadConfig()
	if err != nil {
		panic("config error: " + err.Error())
	}

	// ── Logger ────────────────────────────────────────────────────────
	var log *zap.Logger
	if cfg.App.Env == "production" {
		log, _ = zap.NewProduction()
	} else {
		log, _ = zap.NewDevelopment()
	}
	defer log.Sync()

	log.Info("starting Sailbit POS backend",
		zap.String("version", cfg.App.Version),
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.App.Addr),
	)

	ctx := context.Background()

	// ── Database ──────────────────────────────────────────────────────
	db, err := newDBManager(ctx, &cfg.DB)
	if err != nil {
		log.Fatal("database init failed", zap.Error(err))
	}
	defer db.Close()

	// ── Redis ─────────────────────────────────────────────────────────
	cache, err := newCache(&cfg.Redis)
	if err != nil {
		log.Fatal("redis init failed", zap.Error(err))
	}
	defer cache.Close()

	// ── SSE Broker ────────────────────────────────────────────────────
	broker := newSSEBroker(cache, &cfg.SSE)

	// ── Fiber App ─────────────────────────────────────────────────────
	app := fiber.New(fiber.Config{
		AppName:               "Sailbit POS v" + cfg.App.Version,
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          30 * time.Second,
		IdleTimeout:           120 * time.Second,
		Concurrency:           256 * 1024,
		DisableStartupMessage: cfg.App.Env == "production",
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(Envelope{Success: false, Error: err.Error()})
		},
	})

	// ── Global Middleware ─────────────────────────────────────────────
	app.Use(recover.New(recover.Config{EnableStackTrace: cfg.App.Env != "production"}))
	app.Use(requestid.New())
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-Tenant-Slug",
		AllowMethods: "GET,POST,PUT,PATCH,DELETE,OPTIONS",
	}))

	// Request logger
	app.Use(func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		log.Info("→",
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", c.Response().StatusCode()),
			zap.Duration("latency", time.Since(start)),
			zap.String("ip", c.IP()),
		)
		return err
	})

	// ── Health Check ──────────────────────────────────────────────────
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"version": cfg.App.Version,
			"env":     cfg.App.Env,
		})
	})

	// ── API v1 ────────────────────────────────────────────────────────
	// All routes below require tenant resolution + rate limiting
	api := app.Group("/api/v1",
		resolveTenant(db, cache),
		rateLimit(cache, 300, time.Minute),
	)

	// ── Auth (public within tenant context) ───────────────────────────
	api.Post("/auth/login",   handleLogin(db, cache, &cfg.JWT))
	api.Post("/auth/refresh", handleRefresh(cache, &cfg.JWT))
	api.Post("/auth/logout",  requireAuth(&cfg.JWT, cache), handleLogout(cache))

	// ── SSE Stream ────────────────────────────────────────────────────
	// GET /api/v1/events — subscribe to real-time outlet events
	api.Get("/events",
		requireAuth(&cfg.JWT, cache),
		broker.ServeSSE,
	)

	// ── Protected Routes ──────────────────────────────────────────────
	// All below: tenant resolved + authenticated + tenant DB injected
	v1 := api.Group("/",
		requireAuth(&cfg.JWT, cache),
		injectTenantDB(db),
	)

	// ── Products ──────────────────────────────────────────────────────
	v1.Get("/products",                 handleListProducts)
	v1.Get("/products/barcode/:barcode", handleGetProductByBarcode)
	v1.Get("/products/:id",             handleGetProduct)
	v1.Post("/products",                requirePermission("products.create"), handleCreateProduct)
	v1.Patch("/products/:id",           requirePermission("products.update"), handleUpdateProduct)

	// ── Orders ────────────────────────────────────────────────────────
	v1.Get("/orders",             handleListOrders)
	v1.Post("/orders",            handleCreateOrder(broker, cache, log))
	v1.Get("/orders/:id",         handleGetOrder)
	v1.Patch("/orders/:id/complete", handleCompleteOrder(broker, log))
	v1.Patch("/orders/:id/void",     requirePermission("orders.void"), handleVoidOrder(broker))

	// ── Start & Graceful Shutdown ─────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Info("server listening", zap.String("addr", cfg.App.Addr))
		if err := app.Listen(cfg.App.Addr); err != nil {
			log.Error("server error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutting down gracefully...")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := app.ShutdownWithContext(shutCtx); err != nil {
		log.Error("shutdown error", zap.Error(err))
	}

	log.Info("server stopped")
}