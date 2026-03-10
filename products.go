package main

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GET /api/v1/products
func handleListProducts(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	outletID := c.Locals("outlet_id").(string)

	page := max(1, c.QueryInt("page", 1))
	perPage := clamp(c.QueryInt("per_page", 50), 1, 100)
	search := c.Query("q")
	categoryID := c.Query("category_id")
	offset := (page - 1) * perPage

	// Build dynamic WHERE
	where := "WHERE (available_outlets IS NULL OR $1::uuid = ANY(available_outlets)) AND is_available = true"
	args := []any{outletID}
	i := 2

	if search != "" {
		where += fmt.Sprintf(" AND (name ILIKE $%d OR sku ILIKE $%d OR barcode = $%d)", i, i, i)
		args = append(args, "%"+search+"%")
		i++
	}
	if categoryID != "" {
		where += fmt.Sprintf(" AND category_id = $%d", i)
		args = append(args, categoryID)
		i++
	}

	// Count
	var total int64
	countQ := "SELECT COUNT(*) FROM products " + where
	pool.QueryRow(c.Context(), countQ, args...).Scan(&total)

	// List
	listQ := fmt.Sprintf(`
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products %s
		ORDER BY name ASC
		LIMIT %d OFFSET %d`, where, perPage, offset)

	rows, err := pool.Query(c.Context(), listQ, args...)
	if err != nil {
		return sendError(c, fiber.StatusInternalServerError, "failed to fetch products")
	}
	defer rows.Close()

	products := make([]*Product, 0)
	for rows.Next() {
		p := &Product{}
		if err := rows.Scan(
			&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
			&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
			&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
			&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			continue
		}
		products = append(products, p)
	}

	totalPages := int(total) / perPage
	if int(total)%perPage > 0 {
		totalPages++
	}

	return sendPage(c, products, &PageMeta{
		Page:       page,
		PerPage:    perPage,
		TotalItems: total,
		TotalPages: totalPages,
	})
}

// GET /api/v1/products/:id
func handleGetProduct(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return sendError(c, fiber.StatusBadRequest, "invalid product id")
	}

	p, err := fetchProductByID(c, pool, id)
	if err != nil {
		return sendError(c, fiber.StatusNotFound, "product not found")
	}
	return sendOK(c, p)
}

// GET /api/v1/products/barcode/:barcode
func handleGetProductByBarcode(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	barcode := c.Params("barcode")

	p := &Product{}
	err := pool.QueryRow(c.Context(), `
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products WHERE barcode = $1 AND is_available = true LIMIT 1`, barcode,
	).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
		&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
		&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
		&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return sendError(c, fiber.StatusNotFound, "product not found")
	}
	return sendOK(c, p)
}

// POST /api/v1/products
func handleCreateProduct(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)

	var p Product
	if err := c.BodyParser(&p); err != nil {
		return sendError(c, fiber.StatusBadRequest, "invalid request body")
	}
	if p.Name == "" || p.BasePrice <= 0 {
		return sendError(c, fiber.StatusUnprocessableEntity, "name and base_price are required")
	}

	p.ID = uuid.New()
	p.IsAvailable = true
	p.ProductType = coalesce(p.ProductType, "simple")

	_, err := pool.Exec(c.Context(), `
		INSERT INTO products (
			id, category_id, sku, barcode, name, description, image_url,
			product_type, base_price, cost_price, unit, track_stock,
			has_modifiers, preparation_time, kitchen_station,
			is_available, tags, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		p.ID, p.CategoryID, p.SKU, p.Barcode, p.Name, p.Description, p.ImageURL,
		p.ProductType, p.BasePrice, p.CostPrice, p.Unit, p.TrackStock,
		p.HasModifiers, p.PreparationTime, p.KitchenStation,
		p.IsAvailable, p.Tags, p.Metadata,
	)
	if err != nil {
		return sendError(c, fiber.StatusInternalServerError, "failed to create product")
	}

	return sendCreated(c, p)
}

// PATCH /api/v1/products/:id
func handleUpdateProduct(c *fiber.Ctx) error {
	pool := c.Locals("db").(*pgxpool.Pool)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return sendError(c, fiber.StatusBadRequest, "invalid product id")
	}

	var p Product
	if err := c.BodyParser(&p); err != nil {
		return sendError(c, fiber.StatusBadRequest, "invalid request body")
	}

	_, err = pool.Exec(c.Context(), `
		UPDATE products SET
			name=$2, description=$3, base_price=$4, cost_price=$5,
			is_available=$6, tags=$7, metadata=$8, updated_at=NOW()
		WHERE id=$1`, id, p.Name, p.Description, p.BasePrice, p.CostPrice, p.IsAvailable, p.Tags, p.Metadata)
	if err != nil {
		return sendError(c, fiber.StatusInternalServerError, "failed to update product")
	}

	p.ID = id
	return sendOK(c, p)
}

// ─── Helper ───────────────────────────────────────────────────────────────

func fetchProductByID(c *fiber.Ctx, pool *pgxpool.Pool, id uuid.UUID) (*Product, error) {
	p := &Product{}
	return p, pool.QueryRow(c.Context(), `
		SELECT id, category_id, sku, barcode, name, description, image_url,
		       product_type, base_price, cost_price, unit, track_stock,
		       has_modifiers, preparation_time, kitchen_station,
		       is_available, tags, metadata, created_at, updated_at
		FROM products WHERE id = $1`, id,
	).Scan(
		&p.ID, &p.CategoryID, &p.SKU, &p.Barcode, &p.Name, &p.Description, &p.ImageURL,
		&p.ProductType, &p.BasePrice, &p.CostPrice, &p.Unit, &p.TrackStock,
		&p.HasModifiers, &p.PreparationTime, &p.KitchenStation,
		&p.IsAvailable, &p.Tags, &p.Metadata, &p.CreatedAt, &p.UpdatedAt,
	)
}

func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}