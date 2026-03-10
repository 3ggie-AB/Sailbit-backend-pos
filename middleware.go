package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// resolveTenant extracts tenant from X-Tenant-Slug header or subdomain,
// then injects *Tenant and schema name into context locals.
func resolveTenant(db *DBManager, cache *Cache) fiber.Handler {
	return func(c *fiber.Ctx) error {
		slug := c.Get("X-Tenant-Slug")
		if slug == "" {
			// Extract from subdomain: kopikenangan.sailbit.app → kopikenangan
			parts := strings.Split(c.Hostname(), ".")
			if len(parts) >= 3 {
				slug = parts[0]
			}
		}
		if slug == "" {
			return sendError(c, fiber.StatusBadRequest, "tenant not identified — set X-Tenant-Slug header")
		}

		// Fast path: Redis cache
		tenant, _ := cache.GetTenantBySlug(c.Context(), slug)

		// Slow path: DB
		if tenant == nil {
			row := db.Platform().QueryRow(c.Context(), `
				SELECT id, slug, name, display_name, business_type_id, plan_id,
				       db_schema_name, custom_domain, logo_url, primary_color,
				       timezone, locale, currency_code, currency_symbol,
				       status, settings, created_at, updated_at
				FROM platform.tenants
				WHERE slug = $1 AND status != 'cancelled'
				LIMIT 1`, slug)

			t := &Tenant{}
			err := row.Scan(
				&t.ID, &t.Slug, &t.Name, &t.DisplayName,
				&t.BusinessTypeID, &t.PlanID, &t.DBSchemaName,
				&t.CustomDomain, &t.LogoURL, &t.PrimaryColor,
				&t.Timezone, &t.Locale, &t.CurrencyCode, &t.CurrencySymbol,
				&t.Status, &t.Settings, &t.CreatedAt, &t.UpdatedAt,
			)
			if err == pgx.ErrNoRows {
				return sendError(c, fiber.StatusNotFound, "tenant not found")
			}
			if err != nil {
				return sendError(c, fiber.StatusInternalServerError, "db error")
			}
			tenant = t
			go cache.SetTenant(c.Context(), tenant)
		}

		if tenant.Status == "suspended" {
			return sendError(c, fiber.StatusForbidden, "tenant account suspended")
		}

		c.Locals("tenant", tenant)
		c.Locals("tenant_id", tenant.ID.String())
		c.Locals("tenant_schema", tenant.DBSchemaName)
		return c.Next()
	}
}

// requireAuth validates Bearer JWT and injects Claims into context.
func requireAuth(cfg *JWTConfig, cache *Cache) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := extractBearer(c)
		if raw == "" {
			return sendError(c, fiber.StatusUnauthorized, "missing authorization token")
		}

		// Fast path: Redis cached session
		claims, _ := cache.GetSession(c.Context(), raw)

		// Slow path: verify JWT signature
		if claims == nil {
			var err error
			claims, err = parseJWT(raw, cfg.AccessSecret)
			if err != nil {
				return sendError(c, fiber.StatusUnauthorized, "invalid or expired token")
			}
		}

		c.Locals("claims", claims)
		c.Locals("user_id", claims.UserID.String())
		c.Locals("outlet_id", claims.OutletID.String())
		c.Locals("role", claims.Role)
		return c.Next()
	}
}

// injectTenantDB resolves the tenant DB pool and injects it into locals.
// Must come after resolveTenant + requireAuth.
func injectTenantDB(db *DBManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		schema, ok := c.Locals("tenant_schema").(string)
		if !ok || schema == "" {
			return sendError(c, fiber.StatusInternalServerError, "tenant schema missing")
		}

		pool, err := db.Tenant(c.Context(), schema)
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "tenant database unavailable")
		}

		c.Locals("db", pool)
		return c.Next()
	}
}

// requirePermission checks role-based access. "owner" and "manager" bypass all checks.
func requirePermission(perm string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*Claims)
		if !ok {
			return sendError(c, fiber.StatusUnauthorized, "unauthorized")
		}
		if claims.Role == "owner" || claims.Role == "manager" {
			return c.Next()
		}
		// TODO: load role permissions from DB/cache and check perm
		// For now, non-owner/manager roles are restricted to safe operations
		if perm == "orders.void" || perm == "products.create" || perm == "products.update" {
			return sendError(c, fiber.StatusForbidden, fmt.Sprintf("permission denied: %s", perm))
		}
		return c.Next()
	}
}

// rateLimit applies per-IP sliding window rate limiting via Redis.
func rateLimit(cache *Cache, limit int, window time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !cache.RateLimit(c.Context(), c.IP(), limit, window) {
			return sendError(c, fiber.StatusTooManyRequests, "rate limit exceeded — slow down")
		}
		return c.Next()
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func extractBearer(c *fiber.Ctx) string {
	auth := c.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return c.Cookies("access_token")
}

func parseJWT(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	mc, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}

	claims := &Claims{}
	claims.UserID, _ = uuid.Parse(mc["user_id"].(string))
	claims.TenantID, _ = uuid.Parse(mc["tenant_id"].(string))
	claims.OutletID, _ = uuid.Parse(mc["outlet_id"].(string))
	claims.Role, _ = mc["role"].(string)
	claims.TokenType, _ = mc["token_type"].(string)
	return claims, nil
}

func sendError(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(Envelope{Success: false, Error: msg})
}

func sendOK(c *fiber.Ctx, data any) error {
	return c.JSON(Envelope{Success: true, Data: data})
}

func sendCreated(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusCreated).JSON(Envelope{Success: true, Data: data})
}

func sendPage(c *fiber.Ctx, data any, meta *PageMeta) error {
	return c.JSON(Envelope{Success: true, Data: data, Meta: meta})
}