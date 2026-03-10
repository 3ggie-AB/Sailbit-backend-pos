# POS Backend — Multi-Tenant

Go + Fiber + PostgreSQL + Redis + SSE

## Stack

| Layer | Tech |
|-------|------|
| Framework | Fiber v2 |
| Database | PostgreSQL 16 (schema-per-tenant) |
| Cache | Redis 7 |
| Auth | JWT (access 15m + refresh 7d) |
| Realtime | SSE via Redis Pub/Sub |
| Logger | Zap |

## Struktur Project

```
pos-backend/
├── cmd/server/          # Entry point
├── internal/
│   ├── config/          # Config loader (Viper, ENV override)
│   ├── database/        # DB pool manager — platform + per-tenant pools
│   ├── cache/           # Redis client — session, config cache, pub/sub
│   ├── domain/          # Shared types, models, constants
│   ├── middleware/       # TenantResolver, Auth, RateLimit, OutletGuard
│   ├── repository/      # DB queries — interfaces + pgx implementations
│   ├── service/         # Business logic — OrderService, AuthService
│   ├── handler/         # HTTP handlers — thin, delegates to services
│   ├── sse/             # SSE broker — Redis pub/sub fan-out
│   └── server/          # Fiber app bootstrap, route wiring
├── pkg/
│   ├── logger/          # Zap wrapper
│   └── response/        # Standard JSON envelope
├── migrations/
│   ├── platform/        # Shared DB schema (tenants, plans, billing)
│   └── tenant/          # Per-tenant schema (outlets, products, orders)
├── config/config.yaml
├── docker-compose.yml
└── Dockerfile
```

## Quick Start

```bash
# 1. Start infrastructure
docker-compose up postgres redis -d

# 2. Run platform migrations
psql $PLATFORM_DSN -f migrations/platform/001_init.sql

# 3. Copy config
cp config/config.yaml config/config.local.yaml
# Edit secrets in config.local.yaml

# 4. Run
go run ./cmd/server

# 5. (Optional) Run with Docker
docker-compose up --build
```

## API Endpoints

### Auth
```
POST /api/v1/auth/login      — Login dengan username+PIN atau password
POST /api/v1/auth/refresh    — Refresh access token
POST /api/v1/auth/logout     — Revoke session
```

### Products
```
GET  /api/v1/products                  — List produk (filter: q, category_id)
GET  /api/v1/products/:id              — Detail produk
GET  /api/v1/products/barcode/:barcode — Lookup by barcode (untuk scanner)
POST /api/v1/products                  — Tambah produk baru
```

### Orders
```
GET   /api/v1/orders           — List orders (filter: status, page)
POST  /api/v1/orders           — Buat order baru
GET   /api/v1/orders/:id       — Detail order
PATCH /api/v1/orders/:id/complete — Selesaikan order + payment
PATCH /api/v1/orders/:id/void     — Void order
```

### SSE (Server-Sent Events)
```
GET /api/v1/events   — Subscribe ke realtime events outlet ini
```

Event types yang dikirim:
- `order.created`    → KDS terima order baru
- `order.completed`  → Order selesai
- `payment.success`  → Payment berhasil
- `stock.alert`      → Stok mendekati habis
- `table.updated`    → Status meja berubah

## Tenant Resolution

Setiap request harus mengidentifikasi tenant via salah satu:
1. Header: `X-Tenant-Slug: indomaret`
2. Subdomain: `indomaret.pos.yourdomain.com`

## Auth Flow

```
POST /auth/login
  Body: { "username": "kasir01", "pin": "1234", "outlet_id": "uuid" }
  
  Response: {
    "access_token": "...",   // Valid 15 menit
    "refresh_token": "...",  // Valid 7 hari
    "expires_in": 900
  }

Semua request protected pakai header:
  Authorization: Bearer {access_token}
```

## Environment Variables

Semua config bisa di-override via ENV dengan prefix `POS_`:
```
POS_APP_ENV=production
POS_APP_ADDR=:8080
POS_DB_PLATFORM_DSN=postgres://...
POS_DB_TENANT_DSN=postgres://...
POS_REDIS_ADDR=redis:6379
POS_JWT_ACCESS_SECRET=your_secret_here
POS_JWT_REFRESH_SECRET=your_refresh_secret_here
```

## Key Design Decisions

1. **Schema-per-tenant**: Setiap tenant punya PostgreSQL schema sendiri (`tenant_indomaret`). Pool koneksi lazily created dan di-cache per schema.

2. **Tenant DB injection**: Middleware `tenantDBMiddleware` resolve schema → buat pool → wire repo & service → inject ke context. Handler tidak tahu soal multi-tenancy.

3. **SSE via Redis Pub/Sub**: Setiap server instance subscribe ke Redis. Event publish → Redis fan-out ke semua instance → instance push ke client SSE yang terhubung. Horizontal scaling ready.

4. **Stok atomik**: `DeductStock` pakai `SELECT FOR UPDATE` dalam satu transaction. Tidak ada race condition antara dua kasir yang jual produk yang sama bersamaan.

5. **Partitioned tables**: `orders`, `outlet_stock_movements`, `audit_logs` dipartisi per tahun/bulan. Query laporan hanya scan partisi yang relevan.

6. **Config hierarchy cache**: Outlet config di-cache di Redis (TTL 5 menit). Resolusi L1→L5 (Global → Business Type → Tenant → Outlet → Terminal) dilakukan sekali, hasilnya di-cache.
