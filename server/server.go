package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	app   *fiber.App
	cfg   *config.Config
	dbMgr *database.Manager
	rdb   *cache.Client
	log   *zap.Logger
}

func New(cfg *config.Config, log *zap.Logger) (*Server, error) {
	ctx := context.Background()

	dbMgr, err := database.NewManager(ctx, &cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	rdb, err := cache.New(&cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}

	broker := sse.NewBroker(&cfg.SSE, rdb, log)

	app := fiber.New(fiber.Config{
		AppName:               "POS Backend v1.0",
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          30 * time.Second,
		IdleTimeout:           120 * time.Second,
		Concurrency:           256 * 1024,
		DisableStartupMessage: cfg.App.Env == "production",
		JSONEncoder:           json.Marshal,
		JSONDecoder:           json.Unmarshal,
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

	app.Use(recover.New(recover.Config{EnableStackTrace: cfg.App.Env != "production"}))
	app.Use(requestid.New())
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-Tenant-Slug",
		AllowMethods: "GET,POST,PUT,PATCH,DELETE,OPTIONS",
	}))

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

	// ── Platform repositories ─────────────────────────────────────────
	tenantRepo := repository.NewTenantRepository(dbMgr.Platform())

	// ── Services ──────────────────────────────────────────────────────
	authSvc := service.NewAuthService(dbMgr.Platform(), rdb, &cfg.JWT)

	// ── Handlers ──────────────────────────────────────────────────────
	authHandler := handler.NewAuthHandler(authSvc, log)
	sseHandler := handler.NewSSEHandler(broker, log)

	// ── Middleware factories ───────────────────────────────────────────
	tenantMW := middleware.TenantResolver(tenantRepo, rdb, log)
	authMW := middleware.Auth(&cfg.JWT, rdb)
	rateMW := middleware.RateLimit(rdb, 300, 60*time.Second)

	// ── Routes ────────────────────────────────────────────────────────

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "version": cfg.App.Version})
	})

	api := app.Group("/api/v1", tenantMW, rateMW)

	auth := api.Group("/auth")
	auth.Post("/login", authHandler.Login)
	auth.Post("/refresh", authHandler.Refresh)
	auth.Post("/logout", authMW, authHandler.Logout)

	api.Get("/events", authMW, sseHandler.Stream)

	// Tenant-scoped routes — handler injected per request via middleware
	tenantDBMW := tenantDBMiddleware(dbMgr, rdb, log, cfg, broker)
	protected := api.Group("/", authMW, tenantDBMW)

	protected.Get("/products", func(c *fiber.Ctx) error {
		return productHandlerFromCtx(c).List(c)
	})
	protected.Get("/products/barcode/:barcode", func(c *fiber.Ctx) error {
		return productHandlerFromCtx(c).GetByBarcode(c)
	})
	protected.Get("/products/:id", func(c *fiber.Ctx) error {
		return productHandlerFromCtx(c).Get(c)
	})
	protected.Post("/products", middleware.RequirePermission("products.create"), func(c *fiber.Ctx) error {
		return productHandlerFromCtx(c).Create(c)
	})

	protected.Get("/orders", func(c *fiber.Ctx) error {
		return orderHandlerFromCtx(c).List(c)
	})
	protected.Post("/orders", func(c *fiber.Ctx) error {
		return orderHandlerFromCtx(c).Create(c)
	})
	protected.Get("/orders/:id", func(c *fiber.Ctx) error {
		return orderHandlerFromCtx(c).Get(c)
	})
	protected.Patch("/orders/:id/complete", func(c *fiber.Ctx) error {
		return orderHandlerFromCtx(c).Complete(c)
	})
	protected.Patch("/orders/:id/void", middleware.RequirePermission("orders.void"), func(c *fiber.Ctx) error {
		return orderHandlerFromCtx(c).Void(c)
	})

	return &Server{
		app:   app,
		cfg:   cfg,
		dbMgr: dbMgr,
		rdb:   rdb,
		log:   log,
	}, nil
}

// tenantDBMiddleware injects tenant-scoped repositories into context.
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
	s.rdb.Close() //nolint:errcheck
	return nil
}