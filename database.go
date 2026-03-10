package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DBManager struct {
	cfg      *DBConfig
	platform *pgxpool.Pool

	mu      sync.RWMutex
	tenants map[string]*pgxpool.Pool // schema_name → pool
}

func newDBManager(ctx context.Context, cfg *DBConfig) (*DBManager, error) {
	platform, err := buildPool(ctx, cfg.PlatformDSN, cfg.PlatformMaxConn, cfg)
	if err != nil {
		return nil, fmt.Errorf("platform pool: %w", err)
	}

	return &DBManager{
		cfg:      cfg,
		platform: platform,
		tenants:  make(map[string]*pgxpool.Pool),
	}, nil
}

// Platform returns the shared platform pool (tenants, billing, plans).
func (m *DBManager) Platform() *pgxpool.Pool {
	return m.platform
}

// Tenant returns (or lazily creates) a pool scoped to tenant schema.
// Sets search_path automatically — all queries hit the right schema.
func (m *DBManager) Tenant(ctx context.Context, schema string) (*pgxpool.Pool, error) {
	m.mu.RLock()
	p, ok := m.tenants[schema]
	m.mu.RUnlock()
	if ok {
		return p, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if p, ok = m.tenants[schema]; ok {
		return p, nil
	}

	dsn := fmt.Sprintf("%s&options=-csearch_path%%3D%s,public", m.cfg.TenantDSN, schema)
	pool, err := buildPool(ctx, dsn, m.cfg.TenantMaxConn, m.cfg)
	if err != nil {
		return nil, fmt.Errorf("tenant pool [%s]: %w", schema, err)
	}

	m.tenants[schema] = pool
	return pool, nil
}

func (m *DBManager) Close() {
	m.platform.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.tenants {
		p.Close()
	}
}

func buildPool(ctx context.Context, dsn string, maxConn int32, cfg *DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}

	poolCfg.MaxConns = maxConn
	poolCfg.MinConns = maxConn / 5
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}

	return pool, nil
}