package handler

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
	"github.com/3ggie-AB/Sailbit-backend-pos/repository"
	"github.com/3ggie-AB/Sailbit-backend-pos/service"
	"github.com/3ggie-AB/Sailbit-backend-pos/sse"
	"github.com/3ggie-AB/Sailbit-backend-pos/pkg/response"
	"go.uber.org/zap"
)

// ─── Auth Handler ─────────────────────────────────────────────────────────

type AuthHandler struct {
	svc *service.AuthService
	log *zap.Logger
}

func NewAuthHandler(svc *service.AuthService, log *zap.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, log: log}
}

// POST /api/v1/auth/login
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req domain.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid request body")
	}

	tenant := c.Locals("tenant").(*domain.Tenant)

	tokens, err := h.svc.Login(c.Context(), tenant.ID, &req)
	if err != nil {
		h.log.Warn("login failed", zap.String("username", req.Username), zap.Error(err))
		return response.Error(c, fiber.StatusUnauthorized, "invalid credentials")
	}

	return response.OK(c, tokens)
}

// POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.BodyParser(&body); err != nil || body.RefreshToken == "" {
		return response.Error(c, fiber.StatusBadRequest, "refresh_token required")
	}

	tokens, err := h.svc.Refresh(c.Context(), body.RefreshToken)
	if err != nil {
		return response.Error(c, fiber.StatusUnauthorized, "invalid refresh token")
	}

	return response.OK(c, tokens)
}

// POST /api/v1/auth/logout
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	token := c.Get("Authorization")
	if len(token) > 7 {
		token = token[7:] // strip "Bearer "
	}
	h.svc.Logout(c.Context(), token) //nolint
	return response.OK(c, fiber.Map{"message": "logged out"})
}

// ─── Product Handler ──────────────────────────────────────────────────────

type ProductHandler struct {
	repo repository.ProductRepository
	log  *zap.Logger
}

func NewProductHandler(repo repository.ProductRepository, log *zap.Logger) *ProductHandler {
	return &ProductHandler{repo: repo, log: log}
}

// GET /api/v1/products
func (h *ProductHandler) List(c *fiber.Ctx) error {
	outletID, err := uuid.Parse(c.Locals("outlet_id").(string))
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid outlet")
	}

	filter := repository.ProductFilter{
		Search:  c.Query("q"),
		Page:    c.QueryInt("page", 1),
		PerPage: c.QueryInt("per_page", 50),
	}

	if catID := c.Query("category_id"); catID != "" {
		id, _ := uuid.Parse(catID)
		filter.CategoryID = &id
	}

	available := true
	filter.IsAvailable = &available // default: only show available products

	products, total, err := h.repo.List(c.Context(), outletID, filter)
	if err != nil {
		h.log.Error("list products", zap.Error(err))
		return response.Error(c, fiber.StatusInternalServerError, "failed to fetch products")
	}

	perPage := filter.PerPage
	totalPages := int(total) / perPage
	if int(total)%perPage > 0 {
		totalPages++
	}

	return response.Paginated(c, products, &response.Meta{
		Page:       filter.Page,
		PerPage:    perPage,
		TotalItems: total,
		TotalPages: totalPages,
	})
}

// GET /api/v1/products/:id
func (h *ProductHandler) Get(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid product id")
	}

	product, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return response.Error(c, fiber.StatusNotFound, "product not found")
	}

	return response.OK(c, product)
}

// GET /api/v1/products/barcode/:barcode
func (h *ProductHandler) GetByBarcode(c *fiber.Ctx) error {
	barcode := c.Params("barcode")
	if barcode == "" {
		return response.Error(c, fiber.StatusBadRequest, "barcode required")
	}

	product, err := h.repo.FindByBarcode(c.Context(), barcode)
	if err != nil {
		return response.Error(c, fiber.StatusNotFound, "product not found")
	}

	return response.OK(c, product)
}

// POST /api/v1/products
func (h *ProductHandler) Create(c *fiber.Ctx) error {
	var p domain.Product
	if err := c.BodyParser(&p); err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid request body")
	}

	p.ID = uuid.New()
	p.IsAvailable = true

	if err := h.repo.Create(c.Context(), &p); err != nil {
		h.log.Error("create product", zap.Error(err))
		return response.Error(c, fiber.StatusInternalServerError, "failed to create product")
	}

	return response.Created(c, p)
}

// ─── Order Handler ────────────────────────────────────────────────────────

type OrderHandler struct {
	svc  *service.OrderService
	repo repository.OrderRepository
	log  *zap.Logger
}

func NewOrderHandler(svc *service.OrderService, repo repository.OrderRepository, log *zap.Logger) *OrderHandler {
	return &OrderHandler{svc: svc, repo: repo, log: log}
}

// POST /api/v1/orders
func (h *OrderHandler) Create(c *fiber.Ctx) error {
	claims := c.Locals("claims").(*domain.Claims)

	var req service.CreateOrderRequest
	if err := c.BodyParser(&req); err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid request body")
	}

	// Ensure outlet matches session
	req.OutletID = claims.OutletID

	order, err := h.svc.CreateOrder(c.Context(), claims.TenantID, claims.UserID, &req)
	if err != nil {
		h.log.Warn("create order failed", zap.Error(err))
		return response.Error(c, fiber.StatusBadRequest, err.Error())
	}

	return response.Created(c, order)
}

// GET /api/v1/orders/:id
func (h *OrderHandler) Get(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid order id")
	}

	order, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return response.Error(c, fiber.StatusNotFound, "order not found")
	}

	return response.OK(c, order)
}

// GET /api/v1/orders
func (h *OrderHandler) List(c *fiber.Ctx) error {
	claims := c.Locals("claims").(*domain.Claims)

	filter := repository.OrderFilter{
		Page:    c.QueryInt("page", 1),
		PerPage: c.QueryInt("per_page", 20),
	}

	orders, total, err := h.repo.ListByOutlet(c.Context(), claims.OutletID, filter)
	if err != nil {
		return response.Error(c, fiber.StatusInternalServerError, "failed to fetch orders")
	}

	return response.Paginated(c, orders, &response.Meta{
		Page:       filter.Page,
		PerPage:    filter.PerPage,
		TotalItems: total,
	})
}

// PATCH /api/v1/orders/:id/complete
func (h *OrderHandler) Complete(c *fiber.Ctx) error {
	claims := c.Locals("claims").(*domain.Claims)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid order id")
	}

	var req struct {
		Payments []service.PaymentRequest `json:"payments" validate:"required,min=1"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid request body")
	}

	order, err := h.svc.CompleteOrder(c.Context(), claims.TenantID, id, req.Payments)
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, err.Error())
	}

	return response.OK(c, order)
}

// PATCH /api/v1/orders/:id/void
func (h *OrderHandler) Void(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return response.Error(c, fiber.StatusBadRequest, "invalid order id")
	}

	// Requires void permission
	order, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return response.Error(c, fiber.StatusNotFound, "order not found")
	}

	if order.Status == domain.OrderStatusVoided {
		return response.Error(c, fiber.StatusBadRequest, "order already voided")
	}

	if err := h.repo.UpdateStatus(c.Context(), id, domain.OrderStatusVoided); err != nil {
		return response.Error(c, fiber.StatusInternalServerError, "failed to void order")
	}

	return response.OK(c, fiber.Map{"message": "order voided"})
}

// ─── SSE Handler ──────────────────────────────────────────────────────────

type SSEHandler struct {
	broker *sse.Broker
	log    *zap.Logger
}

func NewSSEHandler(broker *sse.Broker, log *zap.Logger) *SSEHandler {
	return &SSEHandler{broker: broker, log: log}
}

// GET /api/v1/events  (SSE endpoint)
func (h *SSEHandler) Stream(c *fiber.Ctx) error {
	return h.broker.ServeHTTP(c)
}
