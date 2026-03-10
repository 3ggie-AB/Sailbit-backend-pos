package service

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/3ggie-AB/Sailbit-backend-pos/cache"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	db    *pgxpool.Pool
	cache *cache.Client
	cfg   *config.JWTConfig
}

func NewAuthService(db *pgxpool.Pool, cache *cache.Client, cfg *config.JWTConfig) *AuthService {
	return &AuthService{db: db, cache: cache, cfg: cfg}
}

func (s *AuthService) Login(ctx context.Context, tenantID uuid.UUID, req *domain.LoginRequest) (*domain.TokenPair, error) {
	outletID, err := uuid.Parse(req.OutletID)
	if err != nil {
		return nil, fmt.Errorf("invalid outlet_id")
	}

	// Fetch user by username within this tenant's schema (search_path already set)
	const q = `
		SELECT id, role_id, pin_hash, password_hash, is_active, outlet_ids,
		       r.code as role_code
		FROM users u
		JOIN roles r ON r.id = u.role_id
		WHERE u.username = $1 AND u.is_active = true
		LIMIT 1`

	var (
		userID       uuid.UUID
		roleID       uuid.UUID
		pinHash      *string
		passwordHash *string
		isActive     bool
		outletIDs    []uuid.UUID
		roleCode     string
	)

	err = s.db.QueryRow(ctx, q, req.Username).Scan(
		&userID, &roleID, &pinHash, &passwordHash, &isActive, &outletIDs, &roleCode,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	// Verify PIN or password
	if req.PIN != "" {
		if pinHash == nil {
			return nil, fmt.Errorf("PIN login not configured for this user")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*pinHash), []byte(req.PIN)); err != nil {
			return nil, fmt.Errorf("invalid credentials")
		}
	} else if req.Password != "" {
		if passwordHash == nil {
			return nil, fmt.Errorf("password login not configured for this user")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*passwordHash), []byte(req.Password)); err != nil {
			return nil, fmt.Errorf("invalid credentials")
		}
	} else {
		return nil, fmt.Errorf("pin or password required")
	}

	// Check outlet access
	if !hasOutletAccess(outletIDs, outletID) {
		return nil, fmt.Errorf("access to this outlet is not permitted")
	}

	claims := &domain.Claims{
		UserID:   userID,
		TenantID: tenantID,
		OutletID: outletID,
		Role:     roleCode,
	}

	return s.generateTokenPair(ctx, claims)
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*domain.TokenPair, error) {
	// Verify refresh token
	claims, err := verifyJWTClaims(refreshToken, s.cfg.RefreshSecret)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token")
	}

	if claims.TokenType != "refresh" {
		return nil, fmt.Errorf("not a refresh token")
	}

	// Revoke old refresh token
	s.cache.DeleteSession(ctx, refreshToken) //nolint

	claims.TokenType = ""
	return s.generateTokenPair(ctx, claims)
}

func (s *AuthService) Logout(ctx context.Context, accessToken string) error {
	return s.cache.DeleteSession(ctx, accessToken)
}

func (s *AuthService) generateTokenPair(ctx context.Context, claims *domain.Claims) (*domain.TokenPair, error) {
	// Access token
	claims.TokenType = "access"
	accessToken, err := s.signJWT(claims, s.cfg.AccessSecret, s.cfg.AccessTTL)
	if err != nil {
		return nil, err
	}

	// Refresh token
	claims.TokenType = "refresh"
	refreshToken, err := s.signJWT(claims, s.cfg.RefreshSecret, s.cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}

	// Cache access token for fast validation (avoids JWT verify on every request)
	claims.TokenType = "access"
	if err := s.cache.SetSession(ctx, accessToken, claims); err != nil {
		// Non-fatal — JWT verification still works
	}

	return &domain.TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.cfg.AccessTTL.Seconds()),
	}, nil
}

func (s *AuthService) signJWT(claims *domain.Claims, secret string, ttl time.Duration) (string, error) {
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

func verifyJWTClaims(tokenStr, secret string) (*domain.Claims, error) {
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

	claims := &domain.Claims{}
	claims.UserID, _ = uuid.Parse(mc["user_id"].(string))
	claims.TenantID, _ = uuid.Parse(mc["tenant_id"].(string))
	claims.OutletID, _ = uuid.Parse(mc["outlet_id"].(string))
	claims.Role, _ = mc["role"].(string)
	claims.TokenType, _ = mc["token_type"].(string)
	return claims, nil
}

func hasOutletAccess(allowed []uuid.UUID, requested uuid.UUID) bool {
	if len(allowed) == 0 {
		return true // nil = access to all outlets
	}
	for _, id := range allowed {
		if id == requested {
			return true
		}
	}
	return false
}
