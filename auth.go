package main

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// POST /api/v1/auth/login
func handleLogin(db *DBManager, cache *Cache, cfg *JWTConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req LoginRequest
		if err := c.BodyParser(&req); err != nil {
			return sendError(c, fiber.StatusBadRequest, "invalid request body")
		}

		tenant := c.Locals("tenant").(*Tenant)

		outletID, err := uuid.Parse(req.OutletID)
		if err != nil {
			return sendError(c, fiber.StatusBadRequest, "invalid outlet_id")
		}

		// Get tenant DB pool
		pool, err := db.Tenant(c.Context(), tenant.DBSchemaName)
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "db unavailable")
		}

		// Fetch user
		var (
			userID       uuid.UUID
			pinHash      *string
			passwordHash *string
			outletIDs    []uuid.UUID
			roleCode     string
		)

		err = pool.QueryRow(c.Context(), `
			SELECT u.id, u.pin_hash, u.password_hash, u.outlet_ids, r.code
			FROM users u
			JOIN roles r ON r.id = u.role_id
			WHERE u.username = $1 AND u.is_active = true
			LIMIT 1`, req.Username,
		).Scan(&userID, &pinHash, &passwordHash, &outletIDs, &roleCode)

		if err != nil {
			return sendError(c, fiber.StatusUnauthorized, "invalid credentials")
		}

		// Verify credential
		if req.PIN != "" {
			if pinHash == nil {
				return sendError(c, fiber.StatusUnauthorized, "PIN not configured")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(*pinHash), []byte(req.PIN)); err != nil {
				return sendError(c, fiber.StatusUnauthorized, "invalid credentials")
			}
		} else if req.Password != "" {
			if passwordHash == nil {
				return sendError(c, fiber.StatusUnauthorized, "password not configured")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(*passwordHash), []byte(req.Password)); err != nil {
				return sendError(c, fiber.StatusUnauthorized, "invalid credentials")
			}
		} else {
			return sendError(c, fiber.StatusBadRequest, "pin or password required")
		}

		// Check outlet access
		if !outletAllowed(outletIDs, outletID) {
			return sendError(c, fiber.StatusForbidden, "outlet access not permitted")
		}

		claims := &Claims{
			UserID:   userID,
			TenantID: tenant.ID,
			OutletID: outletID,
			Role:     roleCode,
		}

		tokens, err := generateTokenPair(claims, cfg)
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "token generation failed")
		}

		// Cache access token for fast validation
		claims.TokenType = "access"
		cache.SetSession(c.Context(), tokens.AccessToken, claims)

		return sendOK(c, tokens)
	}
}

// POST /api/v1/auth/refresh
func handleRefresh(cache *Cache, cfg *JWTConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := c.BodyParser(&body); err != nil || body.RefreshToken == "" {
			return sendError(c, fiber.StatusBadRequest, "refresh_token required")
		}

		claims, err := parseJWT(body.RefreshToken, cfg.RefreshSecret)
		if err != nil || claims.TokenType != "refresh" {
			return sendError(c, fiber.StatusUnauthorized, "invalid refresh token")
		}

		// Revoke old token
		cache.DelSession(c.Context(), body.RefreshToken)

		tokens, err := generateTokenPair(claims, cfg)
		if err != nil {
			return sendError(c, fiber.StatusInternalServerError, "token generation failed")
		}

		claims.TokenType = "access"
		cache.SetSession(c.Context(), tokens.AccessToken, claims)

		return sendOK(c, tokens)
	}
}

// POST /api/v1/auth/logout
func handleLogout(cache *Cache) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := extractBearer(c)
		if token != "" {
			cache.DelSession(c.Context(), token)
		}
		return sendOK(c, fiber.Map{"message": "logged out"})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func generateTokenPair(claims *Claims, cfg *JWTConfig) (*TokenPair, error) {
	claims.TokenType = "access"
	access, err := signJWT(claims, cfg.AccessSecret, cfg.AccessTTL)
	if err != nil {
		return nil, err
	}

	claims.TokenType = "refresh"
	refresh, err := signJWT(claims, cfg.RefreshSecret, cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(cfg.AccessTTL.Seconds()),
	}, nil
}

func signJWT(claims *Claims, secret string, ttl time.Duration) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":    claims.UserID.String(),
		"tenant_id":  claims.TenantID.String(),
		"outlet_id":  claims.OutletID.String(),
		"role":       claims.Role,
		"token_type": claims.TokenType,
		"exp":        time.Now().Add(ttl).Unix(),
		"iat":        time.Now().Unix(),
	})
	return token.SignedString([]byte(secret))
}

func outletAllowed(allowed []uuid.UUID, requested uuid.UUID) bool {
	if len(allowed) == 0 {
		return true // nil = access all outlets
	}
	for _, id := range allowed {
		if id == requested {
			return true
		}
	}
	return false
}

// Suppress unused import warning
var _ = fmt.Sprintf
var _ *pgxpool.Pool