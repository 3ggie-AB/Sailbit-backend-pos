package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
)

// ─── Interfaces ───────────────────────────────────────────────────────────

type TenantRepository interface {
	FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
}

type ProductRepository interface {
	List(ctx context.Context, outletID uuid.UUID, filter ProductFilter) ([]*domain.Product, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Product, error)
	FindByBarcode(ctx context.Context, barcode string) (*domain.Product, error)
	Create(ctx context.Context, p *domain.Product) error
	Update(ctx context.Context, p *domain.Product) error
}

type OrderRepository interface {
	Create(ctx context.Context, order *domain.Order) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	FindByNumber(ctx context.Context, number string) (*domain.Order, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.OrderStatus) error
	AddItem(ctx context.Context, item *domain.OrderItem) error
	ListByOutlet(ctx context.Context, outletID uuid.UUID, filter OrderFilter) ([]*domain.Order, int64, error)
}

type StockRepository interface {
	GetStock(ctx context.Context, outletID, productID uuid.UUID) (float64, error)
	DeductStock(ctx context.Context, outletID, productID uuid.UUID, qty float64, ref string, refID uuid.UUID) error
	AddMovement(ctx context.Context, m *StockMovement) error
}

// ─── Filter types ─────────────────────────────────────────────────────────

type ProductFilter struct {
	Search     string
	CategoryID *uuid.UUID
	IsAvailable *bool
	Page       int
	PerPage    int
}

type OrderFilter struct {
	Status    *domain.OrderStatus
	CashierID *uuid.UUID
	DateFrom  *string
	DateTo    *string
	Page      int
	PerPage   int
}

type StockMovement struct {
	OutletID      uuid.UUID
	ProductID     uuid.UUID
	VariantID     *uuid.UUID
	MovementType  string
	QtyChange     float64
	QtyBefore     float64
	QtyAfter      float64
	ReferenceType string
	ReferenceID   *uuid.UUID
	Notes         *string
	CreatedBy     *uuid.UUID
}

// ─── Platform: Tenant repository ──────────────────────────────────────────

type pgTenantRepo struct {
	db *pgxpool.Pool
}

func NewTenantRepository(db *pgxpool.Pool) TenantRepository {
	return &pgTenantRepo{db: db}
}

func (r *pgTenantRepo) FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	const q = `
		SELECT id, slug, name, display_name, business_type_id, plan_id,
		       db_schema_name, custom_domain, logo_url, primary_color, secondary_color,
		       timezone, locale, currency_code, currency_symbol,
		       status, settings, created_at, updated_at
		FROM platform.tenants
		WHERE slug = $1 AND status != 'cancelled'
		LIMIT 1`

	row := r.db.QueryRow(ctx, q, slug)
	return scanTenant(row)
}

func (r *pgTenantRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	const q = `
		SELECT id, slug, name, display_name, business_type_id, plan_id,
		       db_schema_name, custom_domain, logo_url, primary_color, secondary_color,
		       timezone, locale, currency_code, currency_symbol,
		       status, settings, created_at, updated_at
		FROM platform.tenants
		WHERE id = $1
		LIMIT 1`

	row := r.db.QueryRow(ctx, q, id)
	return scanTenant(row)
}

func scanTenant(row pgx.Row) (*domain.Tenant, error) {
	t := &domain.Tenant{}
	err := row.Scan(
		&t.ID, &t.Slug, &t.Name, &t.DisplayName,
		&t.BusinessTypeID, &t.PlanID, &t.DBSchemaName,
		&t.CustomDomain, &t.LogoURL, &t.PrimaryColor, &t.SecondaryColor,
		&t.Timezone, &t.Locale, &t.CurrencyCode, &t.CurrencySymbol,
		&t.Status, &t.Settings, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("tenant not found")
		}
		return nil, err
	}
	return t, nil
}

// ─── Tenant: Product repository ───────────────────────────────────────────

type pgProductRepo struct {
	db *pgxpool.Pool
}

func NewProductRepository(db *pgxpool.Pool) ProductRepository {
	return &pgProductRepo{db: db}
}

func (r *pgProductRepo) List(ctx context.Context, outletID uuid.UUID, f ProductFilter) ([]*domain.Product, int64, error) {
	// Dynamic query with optional filters — using pgx named args
	where := "WHERE (available_outlets IS NULL OR $1 = ANY(available_outlets))"
	args := pgx.NamedArgs{"outlet_id": outletID}
	argIdx := 2

	if f.Search != "" {
		where += fmt.Sprintf(" AND (name ILIKE $%d OR sku ILIKE $%d OR barcode = $%d)", argIdx, argIdx, argIdx)
		args[fmt.Sprintf("p%d", argIdx)] = "%" + f.Search + "%"
		argIdx++
	}
	if f.CategoryID != nil {
		where += fmt.Sprintf(" AND category_id = $%d", argIdx)
		args[fmt.Sprintf("p%d", argIdx)] = *f.CategoryID
		argIdx++
	}
	if f.IsAvailable != nil {
		where += fmt.Sprintf(" AND is_available = $%d", argIdx)
		args[fmt.Sprintf("p%d", argIdx)] = *f.IsAvailable
		argIdx++
	}

	page := max(1, f.Page)
	perPage := max(10, min(100, f.PerPage))
	offset := (page - 1) * perPage

	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM products %s`, where)
	listQ := fmt.Sprintf(`
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products %s
		ORDER BY name ASC
		LIMIT %d OFFSET %d`, where, perPage, offset)

	var total int64
	if err := r.db.QueryRow(ctx, countQ, outletID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx, listQ, outletID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var products []*domain.Product
	for rows.Next() {
		p := &domain.Product{}
		if err := rows.Scan(
			&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
			&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
			&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
			&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		products = append(products, p)
	}

	return products, total, rows.Err()
}

func (r *pgProductRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
	const q = `
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products WHERE id = $1`

	p := &domain.Product{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
		&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
		&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
		&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgProductRepo) FindByBarcode(ctx context.Context, barcode string) (*domain.Product, error) {
	const q = `
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products WHERE barcode = $1 AND is_available = true LIMIT 1`

	p := &domain.Product{}
	err := r.db.QueryRow(ctx, q, barcode).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
		&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
		&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
		&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgProductRepo) Create(ctx context.Context, p *domain.Product) error {
	const q = `
		INSERT INTO products (
			id, category_id, sku, barcode, name, description, image_url,
			product_type, base_price, cost_price, unit, track_stock,
			has_modifiers, preparation_time, kitchen_station,
			is_available, tags, metadata
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
		)`

	_, err := r.db.Exec(ctx, q,
		p.ID, p.CategoryID, p.SKU, p.Barcode, p.Name, p.Description, p.ImageURL,
		p.ProductType, p.BasePrice, p.CostPrice, p.Unit, p.TrackStock,
		p.HasModifiers, p.PreparationTime, p.KitchenStation,
		p.IsAvailable, p.Tags, p.Metadata,
	)
	return err
}

func (r *pgProductRepo) Update(ctx context.Context, p *domain.Product) error {
	const q = `
		UPDATE products SET
			name=$2, description=$3, base_price=$4, cost_price=$5,
			is_available=$6, tags=$7, metadata=$8, updated_at=NOW()
		WHERE id=$1`

	_, err := r.db.Exec(ctx, q,
		p.ID, p.Name, p.Description, p.BasePrice, p.CostPrice,
		p.IsAvailable, p.Tags, p.Metadata,
	)
	return err
}

// ─── Tenant: Order repository ─────────────────────────────────────────────

type pgOrderRepo struct {
	db *pgxpool.Pool
}

func NewOrderRepository(db *pgxpool.Pool) OrderRepository {
	return &pgOrderRepo{db: db}
}

func (r *pgOrderRepo) Create(ctx context.Context, o *domain.Order) error {
	const q = `
		INSERT INTO orders (
			id, order_number, outlet_id, terminal_id, cashier_id, customer_id,
			transaction_type, table_id, status,
			subtotal, discount_amount, tax_amount, service_charge, rounding, total_amount,
			notes, extra_data
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
		)`

	_, err := r.db.Exec(ctx, q,
		o.ID, o.OrderNumber, o.OutletID, o.TerminalID, o.CashierID, o.CustomerID,
		o.TransactionType, o.TableID, o.Status,
		o.Subtotal, o.DiscountAmount, o.TaxAmount, o.ServiceCharge, o.Rounding, o.TotalAmount,
		o.Notes, o.ExtraData,
	)
	return err
}

func (r *pgOrderRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	const q = `
		SELECT id, order_number, outlet_id, terminal_id, cashier_id, customer_id,
		       transaction_type, table_id, status,
		       subtotal, discount_amount, tax_amount, service_charge, rounding, total_amount,
		       notes, extra_data, completed_at, created_at, updated_at
		FROM orders WHERE id = $1`

	o := &domain.Order{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&o.ID, &o.OrderNumber, &o.OutletID, &o.TerminalID, &o.CashierID, &o.CustomerID,
		&o.TransactionType, &o.TableID, &o.Status,
		&o.Subtotal, &o.DiscountAmount, &o.TaxAmount, &o.ServiceCharge, &o.Rounding, &o.TotalAmount,
		&o.Notes, &o.ExtraData, &o.CompletedAt, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (r *pgOrderRepo) FindByNumber(ctx context.Context, number string) (*domain.Order, error) {
	const q = `SELECT id FROM orders WHERE order_number = $1 LIMIT 1`
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, q, number).Scan(&id); err != nil {
		return nil, err
	}
	return r.FindByID(ctx, id)
}

func (r *pgOrderRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.OrderStatus) error {
	q := `UPDATE orders SET status=$2, updated_at=NOW() WHERE id=$1`
	if status == domain.OrderStatusCompleted {
		q = `UPDATE orders SET status=$2, completed_at=NOW(), updated_at=NOW() WHERE id=$1`
	}
	_, err := r.db.Exec(ctx, q, id, status)
	return err
}

func (r *pgOrderRepo) AddItem(ctx context.Context, item *domain.OrderItem) error {
	const q = `
		INSERT INTO order_items (
			id, order_id, product_id, variant_id, product_name,
			unit_price, quantity, modifiers, discount_amount, tax_amount, line_total,
			notes, kitchen_status, assigned_to, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`

	_, err := r.db.Exec(ctx, q,
		item.ID, item.OrderID, item.ProductID, item.VariantID, item.ProductName,
		item.UnitPrice, item.Quantity, item.Modifiers, item.DiscountAmount, item.TaxAmount, item.LineTotal,
		item.Notes, item.KitchenStatus, item.AssignedTo, item.Status,
	)
	return err
}

func (r *pgOrderRepo) ListByOutlet(ctx context.Context, outletID uuid.UUID, f OrderFilter) ([]*domain.Order, int64, error) {
	where := "WHERE outlet_id = $1"
	args := []any{outletID}
	i := 2

	if f.Status != nil {
		where += fmt.Sprintf(" AND status = $%d", i)
		args = append(args, *f.Status)
		i++
	}
	if f.CashierID != nil {
		where += fmt.Sprintf(" AND cashier_id = $%d", i)
		args = append(args, *f.CashierID)
		i++
	}

	page := max(1, f.Page)
	perPage := max(10, min(100, f.PerPage))
	offset := (page - 1) * perPage

	var total int64
	if err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM orders "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listQ := fmt.Sprintf(`
		SELECT id, order_number, outlet_id, cashier_id, transaction_type,
		       status, total_amount, created_at
		FROM orders %s
		ORDER BY created_at DESC LIMIT %d OFFSET %d`, where, perPage, offset)

	rows, err := r.db.Query(ctx, listQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var orders []*domain.Order
	for rows.Next() {
		o := &domain.Order{}
		if err := rows.Scan(
			&o.ID, &o.OrderNumber, &o.OutletID, &o.CashierID,
			&o.TransactionType, &o.Status, &o.TotalAmount, &o.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		orders = append(orders, o)
	}
	return orders, total, rows.Err()
}

// ─── Tenant: Stock repository ─────────────────────────────────────────────

type pgStockRepo struct {
	db *pgxpool.Pool
}

func NewStockRepository(db *pgxpool.Pool) StockRepository {
	return &pgStockRepo{db: db}
}

func (r *pgStockRepo) GetStock(ctx context.Context, outletID, productID uuid.UUID) (float64, error) {
	const q = `SELECT qty_available FROM outlet_stock WHERE outlet_id=$1 AND product_id=$2`
	var qty float64
	err := r.db.QueryRow(ctx, q, outletID, productID).Scan(&qty)
	return qty, err
}

func (r *pgStockRepo) DeductStock(ctx context.Context, outletID, productID uuid.UUID, qty float64, ref string, refID uuid.UUID) error {
	// Atomic deduction using FOR UPDATE to prevent race conditions
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var qtyBefore float64
	const lockQ = `SELECT qty_on_hand FROM outlet_stock WHERE outlet_id=$1 AND product_id=$2 FOR UPDATE`
	if err := tx.QueryRow(ctx, lockQ, outletID, productID).Scan(&qtyBefore); err != nil {
		return fmt.Errorf("stock not found for product")
	}

	qtyAfter := qtyBefore - qty
	const updateQ = `UPDATE outlet_stock SET qty_on_hand=$3, updated_at=NOW() WHERE outlet_id=$1 AND product_id=$2`
	if _, err := tx.Exec(ctx, updateQ, outletID, productID, qtyAfter); err != nil {
		return err
	}

	const movQ = `
		INSERT INTO outlet_stock_movements (
			outlet_id, product_id, movement_type, qty_change, qty_before, qty_after,
			reference_type, reference_id
		) VALUES ($1,$2,'sale',$3,$4,$5,$6,$7)`
	if _, err := tx.Exec(ctx, movQ, outletID, productID, -qty, qtyBefore, qtyAfter, ref, refID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *pgStockRepo) AddMovement(ctx context.Context, m *StockMovement) error {
	const q = `
		INSERT INTO outlet_stock_movements (
			outlet_id, product_id, variant_id, movement_type,
			qty_change, qty_before, qty_after,
			reference_type, reference_id, notes, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

	_, err := r.db.Exec(ctx, q,
		m.OutletID, m.ProductID, m.VariantID, m.MovementType,
		m.QtyChange, m.QtyBefore, m.QtyAfter,
		m.ReferenceType, m.ReferenceID, m.Notes, m.CreatedBy,
	)
	return err
}

// Helpers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
