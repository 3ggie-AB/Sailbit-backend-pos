-- migrations/platform/001_init.sql
-- Run once on the shared database

CREATE SCHEMA IF NOT EXISTS platform;

-- ── Plans ─────────────────────────────────────────────────────────────────
CREATE TABLE platform.plans (
    id                              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                            VARCHAR(50) UNIQUE NOT NULL,
    name                            VARCHAR(100) NOT NULL,
    base_price_monthly              DECIMAL(12,2) NOT NULL DEFAULT 0,
    base_price_yearly               DECIMAL(12,2) NOT NULL DEFAULT 0,
    included_outlets                SMALLINT NOT NULL DEFAULT 1,
    price_per_extra_outlet_monthly  DECIMAL(12,2) NOT NULL DEFAULT 0,
    included_terminals_per_outlet   SMALLINT NOT NULL DEFAULT 1,
    price_per_extra_terminal_monthly DECIMAL(12,2) NOT NULL DEFAULT 0,
    max_outlets                     SMALLINT,
    max_terminals_per_outlet        SMALLINT,
    max_products                    INTEGER,
    allowed_modules                 JSONB NOT NULL DEFAULT '[]',
    features                        JSONB NOT NULL DEFAULT '{}',
    is_public                       BOOLEAN NOT NULL DEFAULT true,
    is_active                       BOOLEAN NOT NULL DEFAULT true,
    sort_order                      SMALLINT NOT NULL DEFAULT 0,
    created_at                      TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ── Business Types ────────────────────────────────────────────────────────
CREATE TABLE platform.business_types (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            VARCHAR(50) UNIQUE NOT NULL,
    name            VARCHAR(100) NOT NULL,
    description     TEXT,
    default_modules JSONB NOT NULL DEFAULT '[]',
    default_config  JSONB,
    icon            VARCHAR(100),
    sort_order      SMALLINT NOT NULL DEFAULT 0,
    is_active       BOOLEAN NOT NULL DEFAULT true
);

-- ── Tenants ───────────────────────────────────────────────────────────────
CREATE TABLE platform.tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            VARCHAR(100) UNIQUE NOT NULL,
    name            VARCHAR(255) NOT NULL,
    display_name    VARCHAR(255),
    business_type_id UUID NOT NULL REFERENCES platform.business_types(id),
    plan_id         UUID NOT NULL REFERENCES platform.plans(id),
    db_schema_name  VARCHAR(100) UNIQUE NOT NULL,
    custom_domain   VARCHAR(255) UNIQUE,
    logo_url        TEXT,
    primary_color   CHAR(7),
    secondary_color CHAR(7),
    timezone        VARCHAR(100) NOT NULL DEFAULT 'Asia/Jakarta',
    locale          VARCHAR(20) NOT NULL DEFAULT 'id_ID',
    currency_code   CHAR(3) NOT NULL DEFAULT 'IDR',
    currency_symbol VARCHAR(10) NOT NULL DEFAULT 'Rp',
    status          VARCHAR(20) NOT NULL DEFAULT 'trial'
                    CHECK (status IN ('active','suspended','trial','cancelled')),
    settings        JSONB,
    metadata        JSONB,
    created_at      TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tenants_slug   ON platform.tenants(slug);
CREATE INDEX idx_tenants_status ON platform.tenants(status);
CREATE INDEX idx_tenants_domain ON platform.tenants(custom_domain) WHERE custom_domain IS NOT NULL;

-- ── Feature Modules ───────────────────────────────────────────────────────
CREATE TABLE platform.feature_modules (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code             VARCHAR(100) UNIQUE NOT NULL,
    name             VARCHAR(200) NOT NULL,
    category         VARCHAR(100) NOT NULL,
    compatible_types JSONB NOT NULL DEFAULT '[]',
    dependencies     JSONB,
    min_plan         VARCHAR(50) NOT NULL DEFAULT 'starter',
    is_core          BOOLEAN NOT NULL DEFAULT false,
    config_schema    JSONB
);

-- ── Tenant Modules ────────────────────────────────────────────────────────
CREATE TABLE platform.tenant_modules (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
    module_id   UUID NOT NULL REFERENCES platform.feature_modules(id),
    is_enabled  BOOLEAN NOT NULL DEFAULT true,
    config      JSONB,
    enabled_at  TIMESTAMP,
    UNIQUE(tenant_id, module_id)
);

-- ── Subscriptions ─────────────────────────────────────────────────────────
CREATE TABLE platform.subscriptions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID UNIQUE NOT NULL REFERENCES platform.tenants(id),
    plan_id              UUID NOT NULL REFERENCES platform.plans(id),
    billing_cycle        VARCHAR(20) NOT NULL DEFAULT 'monthly'
                         CHECK (billing_cycle IN ('monthly','yearly')),
    status               VARCHAR(20) NOT NULL DEFAULT 'trialing'
                         CHECK (status IN ('trialing','active','past_due','cancelled','expired')),
    trial_ends_at        TIMESTAMP,
    current_period_start TIMESTAMP NOT NULL,
    current_period_end   TIMESTAMP NOT NULL,
    cancel_at_period_end BOOLEAN NOT NULL DEFAULT false,
    cancelled_at         TIMESTAMP,
    active_outlet_count  SMALLINT NOT NULL DEFAULT 1,
    active_terminal_count SMALLINT NOT NULL DEFAULT 1,
    payment_method_ref   VARCHAR(255),
    metadata             JSONB,
    created_at           TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_subscriptions_tenant ON platform.subscriptions(tenant_id);
CREATE INDEX idx_subscriptions_status ON platform.subscriptions(status);

-- ── Invoices ──────────────────────────────────────────────────────────────
CREATE TABLE platform.invoices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_number  VARCHAR(50) UNIQUE NOT NULL,
    tenant_id       UUID NOT NULL REFERENCES platform.tenants(id),
    subscription_id UUID NOT NULL REFERENCES platform.subscriptions(id),
    period_start    DATE NOT NULL,
    period_end      DATE NOT NULL,
    subtotal        DECIMAL(12,2) NOT NULL,
    discount_amount DECIMAL(12,2) NOT NULL DEFAULT 0,
    tax_amount      DECIMAL(12,2) NOT NULL DEFAULT 0,
    total_amount    DECIMAL(12,2) NOT NULL,
    amount_paid     DECIMAL(12,2) NOT NULL DEFAULT 0,
    amount_due      DECIMAL(12,2) GENERATED ALWAYS AS (total_amount - amount_paid) STORED,
    status          VARCHAR(20) NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','sent','paid','partial','overdue','void')),
    due_date        DATE NOT NULL,
    generated_at    TIMESTAMP NOT NULL DEFAULT NOW(),
    paid_at         TIMESTAMP
);

CREATE INDEX idx_invoices_tenant  ON platform.invoices(tenant_id);
CREATE INDEX idx_invoices_status  ON platform.invoices(status);

-- ── Seed: Plans ───────────────────────────────────────────────────────────
INSERT INTO platform.plans (code, name, base_price_monthly, base_price_yearly,
    included_outlets, price_per_extra_outlet_monthly,
    included_terminals_per_outlet, price_per_extra_terminal_monthly,
    max_outlets, max_terminals_per_outlet, max_products,
    allowed_modules, sort_order) VALUES
('starter',      'Starter',      0,       0,        1, 0,       1, 0,       1,    1,   500,  '["transaction_core","product_catalog","user_management"]', 1),
('professional', 'Professional', 299000,  2990000,  3, 99000,   2, 49000,   NULL, 5,   NULL, '["transaction_core","product_catalog","user_management","table_management","kds","modifier","loyalty_points","inventory","promo_engine"]', 2),
('enterprise',   'Enterprise',   999000,  9990000, 10, 79000,   5, 39000,   NULL, NULL,NULL, '["*"]', 3);

-- ── Seed: Business Types ──────────────────────────────────────────────────
INSERT INTO platform.business_types (code, name, default_modules, sort_order) VALUES
('fnb',     'F&B / Restoran', '["transaction_core","product_catalog","table_management","kds","modifier"]', 1),
('retail',  'Retail / Toko',  '["transaction_core","product_catalog","inventory","barcode_scanner"]', 2),
('service', 'Jasa / Service', '["transaction_core","product_catalog","queue_management","appointment"]', 3),
('hybrid',  'Hybrid',         '["transaction_core","product_catalog","table_management","inventory"]', 4);
