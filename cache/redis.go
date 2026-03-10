package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
)

type Client struct {
	rdb *redis.Client
	cfg *config.RedisConfig
}

func New(cfg *config.RedisConfig) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,

		// Performance tuning
		MinIdleConns:    cfg.PoolSize / 4,
		ConnMaxIdleTime: 5 * time.Minute,
		DialTimeout:     3 * time.Second,
		ReadTimeout:     2 * time.Second,
		WriteTimeout:    2 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return &Client{rdb: rdb, cfg: cfg}, nil
}

func (c *Client) Close() error { return c.rdb.Close() }

// ─── Key helpers ──────────────────────────────────────────────────────────

func keySession(token string) string              { return "session:" + token }
func keyTenantInfo(slug string) string            { return "tenant:slug:" + slug }
func keyTenantSchema(tenantID string) string      { return "tenant:schema:" + tenantID }
func keyOutletConfig(outletID string) string      { return "config:outlet:" + outletID }
func keySubscriptionStatus(tenantID string) string { return "sub:status:" + tenantID }
func keyRateLimit(ip string) string               { return "ratelimit:" + ip }

// ─── Session ──────────────────────────────────────────────────────────────

func (c *Client) SetSession(ctx context.Context, token string, claims *domain.Claims) error {
	data, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, keySession(token), data, c.cfg.SessionTTL).Err()
}

func (c *Client) GetSession(ctx context.Context, token string) (*domain.Claims, error) {
	data, err := c.rdb.Get(ctx, keySession(token)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var claims domain.Claims
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

func (c *Client) DeleteSession(ctx context.Context, token string) error {
	return c.rdb.Del(ctx, keySession(token)).Err()
}

// ─── Tenant Info ──────────────────────────────────────────────────────────

func (c *Client) SetTenantInfo(ctx context.Context, tenant *domain.Tenant) error {
	data, err := json.Marshal(tenant)
	if err != nil {
		return err
	}
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, keyTenantInfo(tenant.Slug), data, c.cfg.TenantInfoTTL)
	pipe.Set(ctx, keyTenantSchema(tenant.ID.String()), tenant.DBSchemaName, c.cfg.TenantInfoTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *Client) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	data, err := c.rdb.Get(ctx, keyTenantInfo(slug)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var tenant domain.Tenant
	if err := json.Unmarshal(data, &tenant); err != nil {
		return nil, err
	}
	return &tenant, nil
}

func (c *Client) GetTenantSchema(ctx context.Context, tenantID string) (string, error) {
	schema, err := c.rdb.Get(ctx, keyTenantSchema(tenantID)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return schema, nil
}

func (c *Client) InvalidateTenant(ctx context.Context, slug, tenantID string) error {
	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, keyTenantInfo(slug))
	pipe.Del(ctx, keyTenantSchema(tenantID))
	_, err := pipe.Exec(ctx)
	return err
}

// ─── Outlet Config ────────────────────────────────────────────────────────

func (c *Client) SetOutletConfig(ctx context.Context, outletID string, cfg map[string]any) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, keyOutletConfig(outletID), data, c.cfg.ConfigTTL).Err()
}

func (c *Client) GetOutletConfig(ctx context.Context, outletID string) (map[string]any, error) {
	data, err := c.rdb.Get(ctx, keyOutletConfig(outletID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ─── SSE Pub/Sub ──────────────────────────────────────────────────────────

// SSE channel naming: pos:events:{tenant_id}:{outlet_id}
func SSEChannel(tenantID, outletID string) string {
	return fmt.Sprintf("pos:events:%s:%s", tenantID, outletID)
}

func (c *Client) PublishSSE(ctx context.Context, tenantID, outletID string, event *domain.SSEEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return c.rdb.Publish(ctx, SSEChannel(tenantID, outletID), data).Err()
}

func (c *Client) SubscribeSSE(ctx context.Context, tenantID, outletID string) *redis.PubSub {
	return c.rdb.Subscribe(ctx, SSEChannel(tenantID, outletID))
}

// ─── Rate Limiting ────────────────────────────────────────────────────────

// RateLimit returns (current count, allowed). Uses sliding window.
func (c *Client) RateLimit(ctx context.Context, ip string, limit int, window time.Duration) (int64, bool, error) {
	key := keyRateLimit(ip)
	pipe := c.rdb.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, true, err // fail open on redis error
	}
	count := incr.Val()
	return count, count <= int64(limit), nil
}

// ─── Generic helpers ──────────────────────────────────────────────────────

func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

func (c *Client) Raw() *redis.Client { return c.rdb }
