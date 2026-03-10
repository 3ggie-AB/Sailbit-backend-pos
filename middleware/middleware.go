package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/3ggie-AB/Sailbit-backend-pos/cache"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
	"github.com/3ggie-AB/Sailbit-backend-pos/repository"
	"github.com/3ggie-AB/Sailbit-backend-pos/pkg/response"
	"go.uber.org/zap"
	"github.com/google/uuid"
)

// TenantResolver resolves the tenant from subdomain or X-Tenant-ID header,
// then injects tenant info into context locals.
// Order: X-Tenant-ID header > subdomain > custom domain
func TenantResolver(tenantRepo repository.TenantRepository, cache *cache.Client, log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var slug string

		// 1. Header takes priority (for API clients)
		if h := c.Get("X-Tenant-Slug"); h != "" {
			slug = h
		} else {
			// 2. Extract from subdomain: kopikenangan.pos.com → kopikenangan
			host := c.Hostname()
			parts := strings.Split(host, ".")
			if len(parts) >= 3 {
				slug = parts[0]
			}
		}

		if slug == "" {
			return response.Error(c, fiber.StatusBadRequest, "tenant not identified")
		}

		// 3. Cache lookup first
		tenant, err := cache.GetTenantBySlug(c.Context(), slug)
		if err != nil {
			log.Warn("cache get tenant", zap.Error(err))
		}

		// 4. DB fallback
		if tenant == nil {
			tenant, err = tenantRepo.FindBySlug(c.Context(), slug)
			if err != nil {
				return response.Error(c, fiber.StatusNotFound, "tenant not found")
			}
			// Warm cache
			go cache.SetTenantInfo(c.Context(), tenant) //nolint:errcheck
		}

		// 5. Check tenant status
		if tenant.Status == domain.TenantStatusSuspended || tenant.Status == domain.TenantStatusCancelled {
			return response.Error(c, fiber.StatusForbidden, "tenant account is inactive")
		}

		// 6. Inject into locals
		c.Locals("tenant", tenant)
		c.Locals("tenant_id", tenant.ID.String())
		c.Locals("tenant_schema", tenant.DBSchemaName)

		return c.Next()
	}
}

// Auth validates JWT and injects claims into context.
func Auth(cfg *config.JWTConfig, cache *cache.Client) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := extractToken(c)
		if raw == "" {
			return response.Error(c, fiber.StatusUnauthorized, "missing token")
		}

		// Check revocation list in Redis first (fast path)
		claims, err := cache.GetSession(c.Context(), raw)
		if err != nil {
			return response.Error(c, fiber.StatusInternalServerError, "auth error")
		}

		if claims == nil {
			// Verify JWT signature and expiry
			claims, err = verifyJWT(raw, cfg.AccessSecret)
			if err != nil {
				return response.Error(c, fiber.StatusUnauthorized, "invalid token")
			}
		}

		c.Locals("claims", claims)
		c.Locals("user_id", claims.UserID.String())
		c.Locals("outlet_id", claims.OutletID.String())
		c.Locals("role", claims.Role)

		return c.Next()
	}
}

// RequirePermission checks if the user's role has a specific permission.
// Permission format: "resource.action" e.g. "orders.void"
func RequirePermission(perm string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*domain.Claims)
		if !ok {
			return response.Error(c, fiber.StatusUnauthorized, "unauthorized")
		}

		// owner always has all permissions
		if claims.Role == "owner" || claims.Role == "manager" {
			return c.Next()
		}

		// TODO: load role permissions from cache and check
		// For now, just validate role exists
		if claims.Role == "" {
			return response.Error(c, fiber.StatusForbidden, "insufficient permissions: "+perm)
		}

		return c.Next()
	}
}

// RateLimit applies per-IP rate limiting using Redis sliding window.
func RateLimit(cache *cache.Client, limit int, window time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ip := c.IP()
		count, allowed, err := cache.RateLimit(c.Context(), ip, limit, window)
		if err != nil {
			// Fail open — don't block on Redis error
			return c.Next()
		}

		c.Set("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		c.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", max(0, int64(limit)-count)))

		if !allowed {
			return response.Error(c, fiber.StatusTooManyRequests, "rate limit exceeded")
		}
		return c.Next()
	}
}

// OutletGuard ensures the requested outlet_id belongs to the authenticated tenant.
func OutletGuard() fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("claims").(*domain.Claims)
		if !ok {
			return response.Error(c, fiber.StatusUnauthorized, "unauthorized")
		}

		outletID := c.Params("outlet_id")
		if outletID == "" {
			outletID = c.Query("outlet_id")
		}

		if outletID != "" && outletID != claims.OutletID.String() {
			// Manager/owner can access any outlet in their tenant
			if claims.Role != "owner" && claims.Role != "manager" {
				return response.Error(c, fiber.StatusForbidden, "outlet access denied")
			}
		}

		return c.Next()
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func extractToken(c *fiber.Ctx) string {
	// Bearer token from Authorization header
	auth := c.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Fallback: cookie (for web clients)
	return c.Cookies("access_token")
}

func verifyJWT(tokenStr, secret string) (*domain.Claims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}

	claims := &domain.Claims{}
	if v, ok := mapClaims["user_id"].(string); ok {
		claims.UserID, _ = uuid.Parse(v)
	}
	if v, ok := mapClaims["tenant_id"].(string); ok {
		claims.TenantID, _ = uuid.Parse(v)
	}
	if v, ok := mapClaims["outlet_id"].(string); ok {
		claims.OutletID, _ = uuid.Parse(v)
	}
	claims.Role, _ = mapClaims["role"].(string)
	claims.TokenType, _ = mapClaims["token_type"].(string)

	return claims, nil
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
