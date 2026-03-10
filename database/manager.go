package database

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
)

// Manager holds the platform pool and a cache of tenant-scoped pools.
// For schema-per-tenant we reuse the same physical connection pool
// but set search_path per query via a thin wrapper.
type Manager struct {
	cfg      *config.DBConfig
	platform *pgxpool.Pool

	// tenant schema pools — reuse same DSN, different search_path
	mu      sync.RWMutex
	tenants map[string]*pgxpool.Pool // key: schema_name
}

func NewManager(ctx context.Context, cfg *config.DBConfig) (*Manager, error) {
	platformPool, err := buildPool(ctx, cfg.PlatformDSN, cfg.PlatformMaxConn, cfg.PlatformMinConn, cfg)
	if err != nil {
		return nil, fmt.Errorf("platform pool: %w", err)
	}

	return &Manager{
		cfg:      cfg,
		platform: platformPool,
		tenants:  make(map[string]*pgxpool.Pool),
	}, nil
}

// Platform returns the shared platform pool.
func (m *Manager) Platform() *pgxpool.Pool {
	return m.platform
}

// Tenant returns (or lazily creates) a pool scoped to a tenant schema.
// The pool sets search_path so all queries hit the right schema automatically.
func (m *Manager) Tenant(ctx context.Context, schemaName string) (*pgxpool.Pool, error) {
	m.mu.RLock()
	p, ok := m.tenants[schemaName]
	m.mu.RUnlock()
	if ok {
		return p, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if p, ok = m.tenants[schemaName]; ok {
		return p, nil
	}

	// Build DSN with search_path for this tenant schema
	dsn := fmt.Sprintf("%s&search_path=%s,public", m.cfg.TenantDSN, schemaName)
	pool, err := buildPool(ctx, dsn, m.cfg.TenantMaxConn, m.cfg.TenantMinConn, m.cfg)
	if err != nil {
		return nil, fmt.Errorf("tenant pool [%s]: %w", schemaName, err)
	}

	m.tenants[schemaName] = pool
	return pool, nil
}

// Close shuts down all pools gracefully.
func (m *Manager) Close() {
	m.platform.Close()

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.tenants {
		p.Close()
	}
}

func buildPool(ctx context.Context, dsn string, maxConn, minConn int32, cfg *config.DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}

	poolCfg.MaxConns = maxConn
	poolCfg.MinConns = minConn
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	// Prepared statement cache per connection for hot queries
	poolCfg.ConnConfig.DefaultQueryExecMode = 0 // pgx.QueryExecModeCacheStatement

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping failed: %w", err)
	}

	return pool, nil
}
