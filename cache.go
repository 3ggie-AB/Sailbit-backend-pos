package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache struct {
	rdb *redis.Client
	cfg *RedisConfig
}

func newCache(cfg *RedisConfig) (*Cache, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:            cfg.Addr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		PoolSize:        cfg.PoolSize,
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

	return &Cache{rdb: rdb, cfg: cfg}, nil
}

func (c *Cache) Close() error { return c.rdb.Close() }

// ─── Key helpers ──────────────────────────────────────────────────────────

func keySession(token string) string      { return "session:" + token }
func keyTenantSlug(slug string) string    { return "tenant:slug:" + slug }
func keyTenantSchema(id string) string    { return "tenant:schema:" + id }
func keyOutletConfig(id string) string    { return "config:outlet:" + id }
func keyRateLimit(ip string) string       { return "rl:" + ip }
func sseChannel(tID, oID string) string  { return fmt.Sprintf("pos:sse:%s:%s", tID, oID) }

// ─── Session ──────────────────────────────────────────────────────────────

func (c *Cache) SetSession(ctx context.Context, token string, claims *Claims) error {
	data, _ := json.Marshal(claims)
	return c.rdb.Set(ctx, keySession(token), data, c.cfg.SessionTTL).Err()
}

func (c *Cache) GetSession(ctx context.Context, token string) (*Claims, error) {
	data, err := c.rdb.Get(ctx, keySession(token)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var claims Claims
	json.Unmarshal(data, &claims)
	return &claims, nil
}

func (c *Cache) DelSession(ctx context.Context, token string) error {
	return c.rdb.Del(ctx, keySession(token)).Err()
}

// ─── Tenant ───────────────────────────────────────────────────────────────

func (c *Cache) SetTenant(ctx context.Context, t *Tenant) error {
	data, _ := json.Marshal(t)
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, keyTenantSlug(t.Slug), data, c.cfg.TenantInfoTTL)
	pipe.Set(ctx, keyTenantSchema(t.ID.String()), t.DBSchemaName, c.cfg.TenantInfoTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Cache) GetTenantBySlug(ctx context.Context, slug string) (*Tenant, error) {
	data, err := c.rdb.Get(ctx, keyTenantSlug(slug)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Tenant
	json.Unmarshal(data, &t)
	return &t, nil
}

// ─── Outlet Config ────────────────────────────────────────────────────────

func (c *Cache) SetOutletConfig(ctx context.Context, outletID string, cfg map[string]any) error {
	data, _ := json.Marshal(cfg)
	return c.rdb.Set(ctx, keyOutletConfig(outletID), data, c.cfg.ConfigTTL).Err()
}

func (c *Cache) GetOutletConfig(ctx context.Context, outletID string) map[string]any {
	data, err := c.rdb.Get(ctx, keyOutletConfig(outletID)).Bytes()
	if err != nil {
		return nil
	}
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	return cfg
}

// ─── SSE Pub/Sub ──────────────────────────────────────────────────────────

func (c *Cache) PublishSSE(ctx context.Context, tenantID, outletID string, event *SSEEvent) error {
	data, _ := json.Marshal(event)
	return c.rdb.Publish(ctx, sseChannel(tenantID, outletID), data).Err()
}

func (c *Cache) SubscribeSSE(ctx context.Context, tenantID, outletID string) *redis.PubSub {
	return c.rdb.Subscribe(ctx, sseChannel(tenantID, outletID))
}

// ─── Rate Limit (sliding window) ─────────────────────────────────────────

func (c *Cache) RateLimit(ctx context.Context, ip string, limit int, window time.Duration) (allowed bool) {
	key := keyRateLimit(ip)
	pipe := c.rdb.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	pipe.Exec(ctx)
	return incr.Val() <= int64(limit)
}