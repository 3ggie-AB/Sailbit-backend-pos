package main

import (
	"time"

	"github.com/google/uuid"
)

// ─── Tenant ───────────────────────────────────────────────────────────────

type Tenant struct {
	ID             uuid.UUID      `json:"id"`
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	DisplayName    string         `json:"display_name"`
	BusinessTypeID uuid.UUID      `json:"business_type_id"`
	PlanID         uuid.UUID      `json:"plan_id"`
	DBSchemaName   string         `json:"-"`
	CustomDomain   *string        `json:"custom_domain,omitempty"`
	LogoURL        *string        `json:"logo_url,omitempty"`
	PrimaryColor   *string        `json:"primary_color,omitempty"`
	Timezone       string         `json:"timezone"`
	Locale         string         `json:"locale"`
	CurrencyCode   string         `json:"currency_code"`
	CurrencySymbol string         `json:"currency_symbol"`
	Status         string         `json:"status"`
	Settings       map[string]any `json:"settings,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// ─── Outlet ───────────────────────────────────────────────────────────────

type Outlet struct {
	ID               uuid.UUID      `json:"id"`
	Code             string         `json:"code"`
	Name             string         `json:"name"`
	OutletType       string         `json:"outlet_type"`
	Address          *string        `json:"address,omitempty"`
	City             *string        `json:"city,omitempty"`
	Phone            *string        `json:"phone,omitempty"`
	OperatingHours   map[string]any `json:"operating_hours,omitempty"`
	Config           map[string]any `json:"config,omitempty"`
	TaxIncluded      bool           `json:"tax_included"`
	ServiceChargePct *float64       `json:"service_charge_pct,omitempty"`
	IsActive         bool           `json:"is_active"`
	CreatedAt        time.Time      `json:"created_at"`
}

// ─── User & Auth ──────────────────────────────────────────────────────────

type User struct {
	ID           uuid.UUID   `json:"id"`
	EmployeeCode *string     `json:"employee_code,omitempty"`
	FullName     string      `json:"full_name"`
	Username     string      `json:"username"`
	Email        *string     `json:"email,omitempty"`
	PinHash      *string     `json:"-"`
	PasswordHash *string     `json:"-"`
	RoleID       uuid.UUID   `json:"role_id"`
	RoleCode     string      `json:"role_code"`
	OutletIDs    []uuid.UUID `json:"outlet_ids,omitempty"`
	IsActive     bool        `json:"is_active"`
	CreatedAt    time.Time   `json:"created_at"`
}

type Claims struct {
	UserID    uuid.UUID `json:"user_id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	OutletID  uuid.UUID `json:"outlet_id"`
	Role      string    `json:"role"`
	TokenType string    `json:"token_type"`
}

type LoginRequest struct {
	Username string `json:"username"`
	PIN      string `json:"pin,omitempty"`
	Password string `json:"password,omitempty"`
	OutletID string `json:"outlet_id"`
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ─── Product ──────────────────────────────────────────────────────────────

type Product struct {
	ID              uuid.UUID      `json:"id"`
	CategoryID      uuid.UUID      `json:"category_id"`
	SKU             *string        `json:"sku,omitempty"`
	Barcode         *string        `json:"barcode,omitempty"`
	Name            string         `json:"name"`
	Description     *string        `json:"description,omitempty"`
	ImageURL        *string        `json:"image_url,omitempty"`
	ProductType     string         `json:"product_type"`
	BasePrice       float64        `json:"base_price"`
	CostPrice       *float64       `json:"cost_price,omitempty"`
	Unit            *string        `json:"unit,omitempty"`
	TrackStock      bool           `json:"track_stock"`
	HasModifiers    bool           `json:"has_modifiers"`
	PreparationTime *int           `json:"preparation_time,omitempty"`
	KitchenStation  *string        `json:"kitchen_station,omitempty"`
	IsAvailable     bool           `json:"is_available"`
	Tags            []string       `json:"tags,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// ─── Order ────────────────────────────────────────────────────────────────

type Order struct {
	ID              uuid.UUID      `json:"id"`
	OrderNumber     string         `json:"order_number"`
	OutletID        uuid.UUID      `json:"outlet_id"`
	TerminalID      *uuid.UUID     `json:"terminal_id,omitempty"`
	CashierID       uuid.UUID      `json:"cashier_id"`
	CustomerID      *uuid.UUID     `json:"customer_id,omitempty"`
	TransactionType string         `json:"transaction_type"`
	TableID         *uuid.UUID     `json:"table_id,omitempty"`
	Status          string         `json:"status"`
	Subtotal        float64        `json:"subtotal"`
	DiscountAmount  float64        `json:"discount_amount"`
	TaxAmount       float64        `json:"tax_amount"`
	ServiceCharge   float64        `json:"service_charge"`
	Rounding        float64        `json:"rounding"`
	TotalAmount     float64        `json:"total_amount"`
	Notes           *string        `json:"notes,omitempty"`
	ExtraData       map[string]any `json:"extra_data,omitempty"`
	Items           []OrderItem    `json:"items,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type OrderItem struct {
	ID             uuid.UUID      `json:"id"`
	OrderID        uuid.UUID      `json:"order_id"`
	ProductID      uuid.UUID      `json:"product_id"`
	VariantID      *uuid.UUID     `json:"variant_id,omitempty"`
	ProductName    string         `json:"product_name"`
	UnitPrice      float64        `json:"unit_price"`
	Quantity       float64        `json:"quantity"`
	Modifiers      []ModifierItem `json:"modifiers,omitempty"`
	DiscountAmount float64        `json:"discount_amount"`
	TaxAmount      float64        `json:"tax_amount"`
	LineTotal      float64        `json:"line_total"`
	Notes          *string        `json:"notes,omitempty"`
	KitchenStatus  *string        `json:"kitchen_status,omitempty"`
	AssignedTo     *uuid.UUID     `json:"assigned_to,omitempty"`
	Status         string         `json:"status"`
	CreatedAt      time.Time      `json:"created_at"`
}

type ModifierItem struct {
	Group string  `json:"group"`
	Item  string  `json:"item"`
	Price float64 `json:"price"`
}

type Payment struct {
	ID              uuid.UUID  `json:"id"`
	OrderID         uuid.UUID  `json:"order_id"`
	PaymentMethodID uuid.UUID  `json:"payment_method_id"`
	Amount          float64    `json:"amount"`
	TenderedAmount  *float64   `json:"tendered_amount,omitempty"`
	ChangeAmount    *float64   `json:"change_amount,omitempty"`
	ReferenceNumber *string    `json:"reference_number,omitempty"`
	Status          string     `json:"status"`
	ProcessedAt     *time.Time `json:"processed_at,omitempty"`
	ProcessedBy     uuid.UUID  `json:"processed_by"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ─── Request / Response wrappers ──────────────────────────────────────────

type CreateOrderRequest struct {
	TerminalID      *uuid.UUID          `json:"terminal_id"`
	CustomerID      *uuid.UUID          `json:"customer_id"`
	TransactionType string              `json:"transaction_type"`
	TableID         *uuid.UUID          `json:"table_id"`
	Items           []OrderItemRequest  `json:"items"`
	Notes           *string             `json:"notes"`
	ExtraData       map[string]any      `json:"extra_data"`
}

type OrderItemRequest struct {
	ProductID uuid.UUID      `json:"product_id"`
	VariantID *uuid.UUID     `json:"variant_id"`
	Quantity  float64        `json:"quantity"`
	Modifiers []ModifierItem `json:"modifiers"`
	Notes     *string        `json:"notes"`
}

type CompleteOrderRequest struct {
	Payments []PaymentRequest `json:"payments"`
}

type PaymentRequest struct {
	PaymentMethodID uuid.UUID `json:"payment_method_id"`
	Amount          float64   `json:"amount"`
	TenderedAmount  *float64  `json:"tendered_amount"`
	ReferenceNumber *string   `json:"reference_number"`
}

// ─── SSE ──────────────────────────────────────────────────────────────────

type SSEEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// SSE Event type constants
const (
	EventOrderCreated   = "order.created"
	EventOrderCompleted = "order.completed"
	EventOrderVoided    = "order.voided"
	EventKitchenTicket  = "kitchen.ticket"
	EventStockAlert     = "stock.alert"
	EventTableUpdated   = "table.updated"
	EventPaymentSuccess = "payment.success"
)

// ─── Pagination ───────────────────────────────────────────────────────────

type PageMeta struct {
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	TotalItems int64 `json:"total_items"`
	TotalPages int   `json:"total_pages"`
}

type Envelope struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   string    `json:"error,omitempty"`
	Meta    *PageMeta `json:"meta,omitempty"`
}