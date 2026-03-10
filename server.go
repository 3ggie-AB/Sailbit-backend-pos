package server

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"go.uber.org/zap"

	"github.com/3ggie-AB/Sailbit-backend-pos/cache"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/database"
	"github.com/3ggie-AB/Sailbit-backend-pos/handler"
	"github.com/3ggie-AB/Sailbit-backend-pos/middleware"
	"github.com/3ggie-AB/Sailbit-backend-pos/repository"
	"github.com/3ggie-AB/Sailbit-backend-pos/service"
	"github.com/3ggie-AB/Sailbit-backend-pos/sse"
)

type Server struct {
	app    *fiber.App
	cfg    *config.Config
	dbMgr  *database.Manager
	rdb    *cache.Client
	log    *zap.Logger
}

func New(cfg *config.Config, log *zap.Logger) (*Server, error) {
	ctx := context.Background()

	// ── Database ─────────────────────────────────────────────────────
	dbMgr, err := database.NewManager(ctx, &cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	// ── Redis ─────────────────────────────────────────────────────────
	rdb, err := cache.New(&cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}

	// ── SSE Broker ────────────────────────────────────────────────────
	broker := sse.NewBroker(&cfg.SSE, rdb, log)

	// ── Fiber app ─────────────────────────────────────────────────────
	app := fiber.New(fiber.Config{
		AppName:               "POS Backend v1.0",
		ReadTimeout:           10,
		WriteTimeout:          30,
		IdleTimeout:           120,
		Concurrency:           256 * 1024, // 256k concurrent connections
		DisableStartupMessage: cfg.App.Env == "production",

		// Fast JSON encoder
		JSONEncoder: json.Marshal,
		JSONDecoder: json.Unmarshal,

		// Custom error handler
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"success": false,
				"error":   err.Error(),
			})
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
		err := c.Next()
		log.Info("request",
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", c.Response().StatusCode()),
			zap.String("ip", c.IP()),
			zap.String("request_id", c.GetRespHeader("X-Request-Id")),
		)
		return err
	})

	// ── Platform repositories (shared DB) ─────────────────────────────
	tenantRepo := repository.NewTenantRepository(dbMgr.Platform())

	// ── Services ──────────────────────────────────────────────────────
	authSvc := service.NewAuthService(dbMgr.Platform(), rdb, &cfg.JWT)

	// ── Handlers ──────────────────────────────────────────────────────
	authHandler := handler.NewAuthHandler(authSvc, log)
	sseHandler := handler.NewSSEHandler(broker, log)

	// ── Middleware factories ───────────────────────────────────────────
	tenantMW := middleware.TenantResolver(tenantRepo, rdb, log)
	authMW := middleware.Auth(&cfg.JWT, rdb)
	rateMW := middleware.RateLimit(rdb, 300, 60) // 300 req/min per IP

	// ── Routes ────────────────────────────────────────────────────────

	// Health check — no tenant required
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "version": cfg.App.Version})
	})

	// All API routes require tenant resolution
	api := app.Group("/api/v1", tenantMW, rateMW)

	// Auth — public within tenant context
	auth := api.Group("/auth")
	auth.Post("/login", authHandler.Login)
	auth.Post("/refresh", authHandler.Refresh)
	auth.Post("/logout", authMW, authHandler.Logout)

	// SSE stream — authenticated
	api.Get("/events", authMW, sseHandler.Stream)

	// Tenant-scoped routes — all require auth + tenant DB pool
	protected := api.Group("/", authMW, tenantDBMiddleware(dbMgr, rdb, log, cfg, broker))

	// Products
	protected.Get("/products", productHandlerFromCtx(c).List)
	protected.Get("/products/barcode/:barcode", productHandlerFromCtx(c).GetByBarcode)
	protected.Get("/products/:id", productHandlerFromCtx(c).Get)
	protected.Post("/products", middleware.RequirePermission("products.create"), productHandlerFromCtx(c).Create)

	// Orders
	protected.Get("/orders", orderHandlerFromCtx(c).List)
	protected.Post("/orders", orderHandlerFromCtx(c).Create)
	protected.Get("/orders/:id", orderHandlerFromCtx(c).Get)
	protected.Patch("/orders/:id/complete", orderHandlerFromCtx(c).Complete)
	protected.Patch("/orders/:id/void", middleware.RequirePermission("orders.void"), orderHandlerFromCtx(c).Void)

	return &Server{
		app:   app,
		cfg:   cfg,
		dbMgr: dbMgr,
		rdb:   rdb,
		log:   log,
	}, nil
}

// tenantDBMiddleware injects tenant-scoped repositories into context.
// This is the key pattern: each request gets handlers wired to the right DB pool.
func tenantDBMiddleware(
	dbMgr *database.Manager,
	rdb *cache.Client,
	log *zap.Logger,
	cfg *config.Config,
	broker *sse.Broker,
) fiber.Handler {
	return func(c *fiber.Ctx) error {
		schema := c.Locals("tenant_schema").(string)

		pool, err := dbMgr.Tenant(c.Context(), schema)
		if err != nil {
			log.Error("get tenant pool", zap.String("schema", schema), zap.Error(err))
			return fiber.NewError(fiber.StatusInternalServerError, "database unavailable")
		}

		// Wire up tenant-scoped repos and handlers, inject into context
		productRepo := repository.NewProductRepository(pool)
		orderRepo := repository.NewOrderRepository(pool)
		stockRepo := repository.NewStockRepository(pool)

		orderSvc := service.NewOrderService(orderRepo, productRepo, stockRepo, rdb, broker, log)

		c.Locals("product_handler", handler.NewProductHandler(productRepo, log))
		c.Locals("order_handler", handler.NewOrderHandler(orderSvc, orderRepo, log))

		return c.Next()
	}
}

func productHandlerFromCtx(c *fiber.Ctx) *handler.ProductHandler {
	return c.Locals("product_handler").(*handler.ProductHandler)
}

func orderHandlerFromCtx(c *fiber.Ctx) *handler.OrderHandler {
	return c.Locals("order_handler").(*handler.OrderHandler)
}

func (s *Server) Start() error {
	return s.app.Listen(s.cfg.App.Addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.app.ShutdownWithContext(ctx); err != nil {
		return err
	}
	s.dbMgr.Close()
	s.rdb.Close()
	return nil
}
