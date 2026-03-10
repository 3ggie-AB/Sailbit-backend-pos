package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// POST /api/v1/orders
func handleCreateOrder(broker *SSEBroker, cache *Cache, log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		pool := c.Locals("db").(*pgxpool.Pool)
		claims := c.Locals("claims").(*Claims)

		var req CreateOrderRequest
		if err := c.BodyParser(&req); err != nil {
			return sendError(c, fiber.StatusBadRequest, "invalid request body")
		}
		if len(req.Items) == 0 {
			return sendError(c, fiber.StatusUnprocessableEntity, "order must have at least 1 item")
		}
		if req.TransactionType == "" {
			req.TransactionType = "walkin"
		}

		order := &Order{
			ID:              uuid.New(),
			OrderNumber:     genOrderNumber(),
			OutletID:        claims.OutletID,
			TerminalID:      req.TerminalID,
			CashierID:       claims.UserID,
			CustomerID:      req.CustomerID,
			TransactionType: req.TransactionType,
			TableID:         req.TableID,
			Status:          "confirmed",
			Notes:           req.Notes,
			ExtraData:       req.ExtraData,
		}

		// ── Build items ────────────────────────────────────────────────
		for _, ir := range req.Items {
			if ir.Quantity <= 0 {
				return sendError(c, fiber.StatusUnprocessableEntity, "quantity must be > 0")
			}

			product, err := fetchProductByID(c, pool, ir.ProductID)
			if err != nil {
				return sendError(c, fiber.StatusBadRequest, fmt.Sprintf("product %s not found", ir.ProductID))
			}
			if !product.IsAvailable {
				return sendError(c, fiber.StatusBadRequest, fmt.Sprintf("product '%s' is not available", product.Name))
			}

			// Check stock
			if product.TrackStock {
				var qty float64
				pool.QueryRow(c.Context(), `
					SELECT COALESCE(qty_available, 0)
					FROM outlet_stock
					WHERE outlet_id=$1 AND product_id=$2`,
					claims.OutletID, product.ID,
				).Scan(&qty)

				if qty < ir.Quantity {
					return sendError(c, fiber.StatusConflict,
						fmt.Sprintf("insufficient stock for '%s' (available: %.0f)", product.Name, qty))
				}
			}

			// Calculate modifier total
			modTotal := 0.0
			for _, m := range ir.Modifiers {
				modTotal += m.Price
			}

			unitPrice := product.BasePrice + modTotal
			lineTotal := unitPrice * ir.Quantity

			kitchenStatus := ""
			if product.KitchenStation != nil {
				kitchenStatus = "pending"
			}

			item := OrderItem{
				ID:          uuid.New(),
				OrderID:     order.ID,
				ProductID:   product.ID,
				VariantID:   ir.VariantID,
				ProductName: product.Name,
				UnitPrice:   unitPrice,
				Quantity:    ir.Quantity,
				Modifiers:   ir.Modifiers,
				LineTotal:   lineTotal,
				Notes:       ir.Notes,
				Status:      "active",
			}
			if kitchenStatus != "" {
				item.KitchenStatus = &kitchenStatus
			}

			order.Items = append(order.Items, item)
			order.Subtotal += lineTotal
		}

		// ── Resolve tax + service charge from outlet config ────────────
		outletCfg := cache.GetOutletConfig(c.Context(), claims.OutletID.String())
		taxRate := resolveTaxRate(outletCfg)
		svcPct := resolveServiceCharge(outletCfg)

		order.TaxAmount = order.Subtotal * taxRate
		order.ServiceCharge = order.Subtotal * svcPct
		raw := order.Subtotal + order.TaxAmount + order.ServiceCharge
		order.TotalAmount = roundNearest(raw, 100)
		order.Rounding = order.TotalAmount - raw

		// ── Persist ────────────────────────────────────────────────────
		tx, err := pool.Begin(c.Context())
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "transaction error")
		}
		defer tx.Rollback(c.Context())

		_, err = tx.Exec(c.Context(), `
			INSERT INTO orders (
				id, order_number, outlet_id, terminal_id, cashier_id, customer_id,
				transaction_type, table_id, status,
				subtotal, discount_amount, tax_amount, service_charge, rounding, total_amount,
				notes, extra_data
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			order.ID, order.OrderNumber, order.OutletID, order.TerminalID, order.CashierID, order.CustomerID,
			order.TransactionType, order.TableID, order.Status,
			order.Subtotal, order.DiscountAmount, order.TaxAmount, order.ServiceCharge, order.Rounding, order.TotalAmount,
			order.Notes, order.ExtraData,
		)
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "failed to save order")
		}

		for i := range order.Items {
			item := &order.Items[i]
			_, err = tx.Exec(c.Context(), `
				INSERT INTO order_items (
					id, order_id, product_id, variant_id, product_name,
					unit_price, quantity, modifiers, discount_amount, tax_amount, line_total,
					notes, kitchen_status, assigned_to, status
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
				item.ID, item.OrderID, item.ProductID, item.VariantID, item.ProductName,
				item.UnitPrice, item.Quantity, item.Modifiers, item.DiscountAmount, item.TaxAmount, item.LineTotal,
				item.Notes, item.KitchenStatus, item.AssignedTo, item.Status,
			)
			if err != nil {
				return sendError(c, fiber.StatusInternalServerError, "failed to save order item")
			}

			// Deduct stock atomically within same transaction
			product, _ := fetchProductByID(c, pool, item.ProductID)
			if product != nil && product.TrackStock {
				var qtyBefore float64
				tx.QueryRow(c.Context(), `
					SELECT qty_on_hand FROM outlet_stock
					WHERE outlet_id=$1 AND product_id=$2 FOR UPDATE`,
					claims.OutletID, item.ProductID,
				).Scan(&qtyBefore)

				qtyAfter := qtyBefore - item.Quantity
				tx.Exec(c.Context(), `
					UPDATE outlet_stock SET qty_on_hand=$3, updated_at=NOW()
					WHERE outlet_id=$1 AND product_id=$2`,
					claims.OutletID, item.ProductID, qtyAfter,
				)
				tx.Exec(c.Context(), `
					INSERT INTO outlet_stock_movements (
						outlet_id, product_id, movement_type,
						qty_change, qty_before, qty_after, reference_type, reference_id
					) VALUES ($1,$2,'sale',$3,$4,$5,'order',$6)`,
					claims.OutletID, item.ProductID,
					-item.Quantity, qtyBefore, qtyAfter, order.ID,
				)
			}
		}

		if err := tx.Commit(c.Context()); err != nil {
			return sendError(c, fiber.StatusInternalServerError, "commit failed")
		}

		// ── Publish SSE to outlet (KDS, other terminals) ───────────────
		go func() {
			broker.publish(context.Background(), claims.TenantID.String(), claims.OutletID.String(), &SSEEvent{
				Type: EventOrderCreated,
				Payload: map[string]any{
					"order_id":     order.ID,
					"order_number": order.OrderNumber,
					"total":        order.TotalAmount,
					"item_count":   len(order.Items),
					"table_id":     order.TableID,
					"cashier_id":   order.CashierID,
				},
			})
		}()

		log.Info("order created",
			zap.String("order", order.OrderNumber),
			zap.String("outlet", claims.OutletID.String()),
			zap.Float64("total", order.TotalAmount),
		)

		return sendCreated(c, order)
	}
}

// GET /api/v1/orders
func handleListOrders(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	claims := c.Locals("claims").(*Claims)

	page := max(1, c.QueryInt("page", 1))
	perPage := clamp(c.QueryInt("per_page", 20), 1, 100)
	offset := (page - 1) * perPage
	status := c.Query("status")

	where := "WHERE outlet_id = $1"
	args := []any{claims.OutletID}
	i := 2

	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", i)
		args = append(args, status)
		i++
	}

	var total int64
	pool.QueryRow(c.Context(), "SELECT COUNT(*) FROM orders "+where, args...).Scan(&total)

	rows, err := pool.Query(c.Context(), fmt.Sprintf(`
		SELECT id, order_number, outlet_id, cashier_id, transaction_type,
		       status, subtotal, tax_amount, total_amount, created_at
		FROM orders %s ORDER BY created_at DESC LIMIT %d OFFSET %d`,
		where, perPage, offset), args...)
	if err != nil {
		return sendError(c, fiber.StatusInternalServerError, "failed to fetch orders")
	}
	defer rows.Close()

	orders := make([]*Order, 0)
	for rows.Next() {
		o := &Order{}
		rows.Scan(
			&o.ID, &o.OrderNumber, &o.OutletID, &o.CashierID,
			&o.TransactionType, &o.Status, &o.Subtotal, &o.TaxAmount, &o.TotalAmount, &o.CreatedAt,
		)
		orders = append(orders, o)
	}

	totalPages := int(total) / perPage
	if int(total)%perPage > 0 {
		totalPages++
	}

	return sendPage(c, orders, &PageMeta{
		Page: page, PerPage: perPage,
		TotalItems: total, TotalPages: totalPages,
	})
}

// GET /api/v1/orders/:id
func handleGetOrder(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return sendError(c, fiber.StatusBadRequest, "invalid order id")
	}

	o := &Order{}
	err = pool.QueryRow(c.Context(), `
		SELECT id, order_number, outlet_id, terminal_id, cashier_id, customer_id,
		       transaction_type, table_id, status,
		       subtotal, discount_amount, tax_amount, service_charge, rounding, total_amount,
		       notes, extra_data, completed_at, created_at, updated_at
		FROM orders WHERE id = $1`, id,
	).Scan(
		&o.ID, &o.OrderNumber, &o.OutletID, &o.TerminalID, &o.CashierID, &o.CustomerID,
		&o.TransactionType, &o.TableID, &o.Status,
		&o.Subtotal, &o.DiscountAmount, &o.TaxAmount, &o.ServiceCharge, &o.Rounding, &o.TotalAmount,
		&o.Notes, &o.ExtraData, &o.CompletedAt, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return sendError(c, fiber.StatusNotFound, "order not found")
	}

	// Fetch items
	rows, _ := pool.Query(c.Context(), `
		SELECT id, product_id, variant_id, product_name, unit_price,
		       quantity, modifiers, line_total, notes, kitchen_status, status
		FROM order_items WHERE order_id = $1`, id)
	defer rows.Close()

	for rows.Next() {
		item := OrderItem{}
		rows.Scan(
			&item.ID, &item.ProductID, &item.VariantID, &item.ProductName, &item.UnitPrice,
			&item.Quantity, &item.Modifiers, &item.LineTotal, &item.Notes, &item.KitchenStatus, &item.Status,
		)
		o.Items = append(o.Items, item)
	}

	return sendOK(c, o)
}

// PATCH /api/v1/orders/:id/complete
func handleCompleteOrder(broker *SSEBroker, log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		pool := c.Locals("db").(*pgxpool.Pool)
		claims := c.Locals("claims").(*Claims)

		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return sendError(c, fiber.StatusBadRequest, "invalid order id")
		}

		var req CompleteOrderRequest
		if err := c.BodyParser(&req); err != nil || len(req.Payments) == 0 {
			return sendError(c, fiber.StatusBadRequest, "payments required")
		}

		// Fetch current order
		var totalAmount float64
		var status string
		pool.QueryRow(c.Context(), `SELECT status, total_amount FROM orders WHERE id=$1`, id).
			Scan(&status, &totalAmount)

		if status == "" {
			return sendError(c, fiber.StatusNotFound, "order not found")
		}
		if status == "completed" || status == "voided" {
			return sendError(c, fiber.StatusConflict, fmt.Sprintf("order already %s", status))
		}

		// Validate payment total
		totalPaid := 0.0
		for _, p := range req.Payments {
			totalPaid += p.Amount
		}
		if totalPaid < totalAmount {
			return sendError(c, fiber.StatusUnprocessableEntity,
				fmt.Sprintf("insufficient payment: need %.2f, got %.2f", totalAmount, totalPaid))
		}

		// Update order + insert payments
		tx, _ := pool.Begin(c.Context())
		defer tx.Rollback(c.Context())

		now := time.Now()
		tx.Exec(c.Context(), `
			UPDATE orders SET status='completed', completed_at=$2, updated_at=$2 WHERE id=$1`,
			id, now)

		for _, p := range req.Payments {
			changeAmt := 0.0
			if p.TenderedAmount != nil {
				changeAmt = *p.TenderedAmount - p.Amount
				if changeAmt < 0 {
					changeAmt = 0
				}
			}
			tx.Exec(c.Context(), `
				INSERT INTO payments (
					id, order_id, payment_method_id, amount, tendered_amount,
					change_amount, reference_number, status, processed_at, processed_by
				) VALUES ($1,$2,$3,$4,$5,$6,$7,'success',$8,$9)`,
				uuid.New(), id, p.PaymentMethodID, p.Amount, p.TenderedAmount,
				changeAmt, p.ReferenceNumber, now, claims.UserID,
			)
		}

		if err := tx.Commit(c.Context()); err != nil {
			return sendError(c, fiber.StatusInternalServerError, "commit failed")
		}

		go broker.publish(context.Background(), claims.TenantID.String(), claims.OutletID.String(), &SSEEvent{
			Type: EventPaymentSuccess,
			Payload: map[string]any{
				"order_id":   id,
				"total":      totalAmount,
				"total_paid": totalPaid,
				"change":     totalPaid - totalAmount,
			},
		})

		return sendOK(c, fiber.Map{"order_id": id, "status": "completed", "total_paid": totalPaid})
	}
}

// PATCH /api/v1/orders/:id/void
func handleVoidOrder(broker *SSEBroker) fiber.Handler {
	return func(c *fiber.Ctx) error {
		pool := c.Locals("db").(*pgxpool.Pool)
		claims := c.Locals("claims").(*Claims)

		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return sendError(c, fiber.StatusBadRequest, "invalid order id")
		}

		var body struct {
			Reason string `json:"reason"`
		}
		c.BodyParser(&body)

		var status string
		pool.QueryRow(c.Context(), `SELECT status FROM orders WHERE id=$1`, id).Scan(&status)
		if status == "" {
			return sendError(c, fiber.StatusNotFound, "order not found")
		}
		if status == "voided" || status == "completed" {
			return sendError(c, fiber.StatusConflict, fmt.Sprintf("cannot void order in status: %s", status))
		}

		pool.Exec(c.Context(), `
			UPDATE orders SET status='voided', updated_at=NOW() WHERE id=$1`, id)

		go broker.publish(context.Background(), claims.TenantID.String(), claims.OutletID.String(), &SSEEvent{
			Type:    EventOrderVoided,
			Payload: map[string]any{"order_id": id, "reason": body.Reason},
		})

		return sendOK(c, fiber.Map{"order_id": id, "status": "voided"})
	}
}

// ─── Business logic helpers ───────────────────────────────────────────────

func resolveTaxRate(cfg map[string]any) float64 {
	if cfg != nil {
		if rate, ok := cfg["tax.rate"].(float64); ok {
			return rate / 100
		}
	}
	return 0.11 // default PPN 11%
}

func resolveServiceCharge(cfg map[string]any) float64 {
	if cfg != nil {
		if pct, ok := cfg["service_charge_pct"].(float64); ok {
			return pct / 100
		}
	}
	return 0
}

func roundNearest(amount, nearest float64) float64 {
	if nearest == 0 {
		return amount
	}
	return float64(int64((amount+nearest/2)/nearest)) * nearest
}

func genOrderNumber() string {
	now := time.Now()
	return fmt.Sprintf("ORD-%s-%04d", now.Format("20060102"), now.UnixNano()%9999+1)
}