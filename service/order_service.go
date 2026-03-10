package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/3ggie-AB/Sailbit-backend-pos/cache"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
	"github.com/3ggie-AB/Sailbit-backend-pos/repository"
	"github.com/3ggie-AB/Sailbit-backend-pos/sse"
	"go.uber.org/zap"
)

type CreateOrderRequest struct {
	OutletID        uuid.UUID              `json:"outlet_id" validate:"required"`
	TerminalID      *uuid.UUID             `json:"terminal_id"`
	CustomerID      *uuid.UUID             `json:"customer_id"`
	TransactionType domain.TransactionType `json:"transaction_type" validate:"required"`
	TableID         *uuid.UUID             `json:"table_id"`
	Items           []OrderItemRequest     `json:"items" validate:"required,min=1"`
	Notes           *string                `json:"notes"`
	ExtraData       map[string]any         `json:"extra_data"`
}

type OrderItemRequest struct {
	ProductID uuid.UUID              `json:"product_id" validate:"required"`
	VariantID *uuid.UUID             `json:"variant_id"`
	Quantity  float64                `json:"quantity" validate:"required,gt=0"`
	Modifiers []domain.ModifierItem  `json:"modifiers"`
	Notes     *string                `json:"notes"`
}

type OrderService struct {
	orderRepo   repository.OrderRepository
	productRepo repository.ProductRepository
	stockRepo   repository.StockRepository
	cache       *cache.Client
	broker      *sse.Broker
	log         *zap.Logger
}

func NewOrderService(
	orderRepo repository.OrderRepository,
	productRepo repository.ProductRepository,
	stockRepo repository.StockRepository,
	cache *cache.Client,
	broker *sse.Broker,
	log *zap.Logger,
) *OrderService {
	return &OrderService{
		orderRepo:   orderRepo,
		productRepo: productRepo,
		stockRepo:   stockRepo,
		cache:       cache,
		broker:      broker,
		log:         log,
	}
}

func (s *OrderService) CreateOrder(ctx context.Context, tenantID, cashierID uuid.UUID, req *CreateOrderRequest) (*domain.Order, error) {
	order := &domain.Order{
		ID:              uuid.New(),
		OrderNumber:     generateOrderNumber(),
		OutletID:        req.OutletID,
		TerminalID:      req.TerminalID,
		CashierID:       cashierID,
		CustomerID:      req.CustomerID,
		TransactionType: req.TransactionType,
		TableID:         req.TableID,
		Status:          domain.OrderStatusDraft,
		Notes:           req.Notes,
		ExtraData:       req.ExtraData,
	}

	// Build order items and calculate totals
	for _, itemReq := range req.Items {
		product, err := s.productRepo.FindByID(ctx, itemReq.ProductID)
		if err != nil {
			return nil, fmt.Errorf("product %s not found", itemReq.ProductID)
		}

		// Check and reserve stock
		if product.TrackStock {
			qty, err := s.stockRepo.GetStock(ctx, req.OutletID, product.ID)
			if err != nil || qty < itemReq.Quantity {
				return nil, fmt.Errorf("insufficient stock for %s (available: %.2f)", product.Name, qty)
			}
		}

		// Resolve price: outlet override > base price
		unitPrice := product.BasePrice

		// Calculate modifier total
		modifierTotal := 0.0
		for _, mod := range itemReq.Modifiers {
			modifierTotal += mod.Price
		}

		lineTotal := (unitPrice + modifierTotal) * itemReq.Quantity

		item := &domain.OrderItem{
			ID:           uuid.New(),
			OrderID:      order.ID,
			ProductID:    product.ID,
			VariantID:    itemReq.VariantID,
			ProductName:  product.Name,
			UnitPrice:    unitPrice + modifierTotal,
			Quantity:     itemReq.Quantity,
			Modifiers:    itemReq.Modifiers,
			LineTotal:    lineTotal,
			Notes:        itemReq.Notes,
			Status:       "active",
		}

		// Set kitchen status for F&B items
		if product.KitchenStation != nil {
			status := "pending"
			item.KitchenStatus = &status
		}

		order.Items = append(order.Items, *item)
		order.Subtotal += lineTotal
	}

	// Apply tax (resolved from outlet config)
	outletCfg, _ := s.cache.GetOutletConfig(ctx, req.OutletID.String())
	taxRate := resolveTaxRate(outletCfg)
	order.TaxAmount = order.Subtotal * taxRate

	// Service charge
	serviceChargePct := resolveServiceCharge(outletCfg)
	order.ServiceCharge = order.Subtotal * serviceChargePct

	// Rounding
	raw := order.Subtotal + order.TaxAmount + order.ServiceCharge
	order.TotalAmount = roundToNearest(raw, 100) // round to nearest 100 IDR
	order.Rounding = order.TotalAmount - raw

	order.Status = domain.OrderStatusConfirmed

	// Persist order
	if err := s.orderRepo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	// Persist items and deduct stock
	for i := range order.Items {
		item := &order.Items[i]
		if err := s.orderRepo.AddItem(ctx, item); err != nil {
			return nil, fmt.Errorf("add item: %w", err)
		}

		// Deduct stock atomically
		product, _ := s.productRepo.FindByID(ctx, item.ProductID)
		if product != nil && product.TrackStock {
			if err := s.stockRepo.DeductStock(ctx, req.OutletID, item.ProductID, item.Quantity, "order", order.ID); err != nil {
				s.log.Warn("stock deduction failed", zap.String("product", item.ProductID.String()), zap.Error(err))
				// Don't fail the order — log and flag for reconciliation
			}
		}
	}

	// Publish SSE event to outlet (KDS, other terminals)
	go func() {
		event := &domain.SSEEvent{
			Type: domain.SSEEventOrderCreated,
			Payload: map[string]any{
				"order_id":     order.ID,
				"order_number": order.OrderNumber,
				"outlet_id":    order.OutletID,
				"total":        order.TotalAmount,
				"item_count":   len(order.Items),
				"table_id":     order.TableID,
			},
		}
		if err := s.broker.Publish(context.Background(), tenantID.String(), req.OutletID.String(), event); err != nil {
			s.log.Warn("SSE publish failed", zap.Error(err))
		}
	}()

	s.log.Info("order created",
		zap.String("order_id", order.ID.String()),
		zap.String("order_number", order.OrderNumber),
		zap.Float64("total", order.TotalAmount),
	)

	return order, nil
}

func (s *OrderService) CompleteOrder(ctx context.Context, tenantID uuid.UUID, orderID uuid.UUID, payments []PaymentRequest) (*domain.Order, error) {
	order, err := s.orderRepo.FindByID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("order not found")
	}

	if order.Status != domain.OrderStatusConfirmed && order.Status != domain.OrderStatusInProgress {
		return nil, fmt.Errorf("order cannot be completed in status: %s", order.Status)
	}

	// Validate total payment covers order total
	totalPaid := 0.0
	for _, p := range payments {
		totalPaid += p.Amount
	}
	if totalPaid < order.TotalAmount {
		return nil, fmt.Errorf("insufficient payment: need %.2f, got %.2f", order.TotalAmount, totalPaid)
	}

	if err := s.orderRepo.UpdateStatus(ctx, orderID, domain.OrderStatusCompleted); err != nil {
		return nil, fmt.Errorf("update order status: %w", err)
	}

	order.Status = domain.OrderStatusCompleted
	now := time.Now()
	order.CompletedAt = &now

	// Publish SSE
	go func() {
		event := &domain.SSEEvent{
			Type: domain.SSEEventPaymentSuccess,
			Payload: map[string]any{
				"order_id":     order.ID,
				"order_number": order.OrderNumber,
				"total":        order.TotalAmount,
				"total_paid":   totalPaid,
			},
		}
		s.broker.Publish(context.Background(), tenantID.String(), order.OutletID.String(), event) //nolint
	}()

	return order, nil
}

type PaymentRequest struct {
	PaymentMethodID uuid.UUID `json:"payment_method_id"`
	Amount          float64   `json:"amount"`
	TenderedAmount  *float64  `json:"tendered_amount"`
	ReferenceNumber *string   `json:"reference_number"`
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func generateOrderNumber() string {
	now := time.Now()
	return fmt.Sprintf("ORD-%s-%04d", now.Format("20060102"), now.UnixNano()%10000)
}

func resolveTaxRate(cfg map[string]any) float64 {
	if cfg == nil {
		return 0.11 // default PPN 11%
	}
	if rate, ok := cfg["tax.rate"].(float64); ok {
		return rate / 100
	}
	return 0.11
}

func resolveServiceCharge(cfg map[string]any) float64 {
	if cfg == nil {
		return 0
	}
	if pct, ok := cfg["service_charge_pct"].(float64); ok {
		return pct / 100
	}
	return 0
}

func roundToNearest(amount, nearest float64) float64 {
	if nearest == 0 {
		return amount
	}
	return float64(int64((amount+nearest/2)/nearest)) * nearest
}
