-- migrations/tenant/001_init.sql
-- Executed once per tenant schema: SET search_path = tenant_{slug}; then run this.

-- ── Outlets ───────────────────────────────────────────────────────────────
CREATE TABLE outlets (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code              VARCHAR(50) UNIQUE NOT NULL,
    name              VARCHAR(255) NOT NULL,
    outlet_type       VARCHAR(50) NOT NULL DEFAULT 'hybrid'
                      CHECK (outlet_type IN ('dine_in','takeaway','delivery','hybrid','warehouse','kiosk')),
    address           TEXT,
    city              VARCHAR(100),
    phone             VARCHAR(30),
    operating_hours   JSONB,
    config            JSONB,
    modules_override  JSONB,
    tax_included      BOOLEAN NOT NULL DEFAULT false,
    service_charge_pct DECIMAL(5,2),
    is_active         BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMP NOT NULL DEFAULT NOW()
);

-- ── Terminals ─────────────────────────────────────────────────────────────
CREATE TABLE terminals (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    outlet_id     UUID NOT NULL REFERENCES outlets(id),
    code          VARCHAR(50) NOT NULL,
    name          VARCHAR(255) NOT NULL,
    terminal_type VARCHAR(50) NOT NULL DEFAULT 'cashier'
                  CHECK (terminal_type IN ('cashier','kiosk','kds','customer_display','mobile')),
    device_id     VARCHAR(255),
    flow_id       UUID,
    config        JSONB,
    last_seen_at  TIMESTAMP,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    UNIQUE(outlet_id, code)
);

-- ── Roles ─────────────────────────────────────────────────────────────────
CREATE TABLE roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        VARCHAR(100) UNIQUE NOT NULL,
    name        VARCHAR(200) NOT NULL,
    permissions JSONB NOT NULL DEFAULT '{}',
    is_system   BOOLEAN NOT NULL DEFAULT false,
    description TEXT
);

-- ── Users ─────────────────────────────────────────────────────────────────
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    employee_code VARCHAR(50),
    full_name     VARCHAR(255) NOT NULL,
    username      VARCHAR(100) UNIQUE NOT NULL,
    email         VARCHAR(255),
    pin_hash      VARCHAR(255),
    password_hash VARCHAR(255),
    role_id       UUID NOT NULL REFERENCES roles(id),
    outlet_ids    UUID[],  -- NULL = all outlets
    avatar_url    TEXT,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    last_login_at TIMESTAMP,
    metadata      JSONB,
    created_at    TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_role     ON users(role_id);

-- ── Categories ────────────────────────────────────────────────────────────
CREATE TABLE categories (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id           UUID REFERENCES categories(id),
    code                VARCHAR(100),
    name                VARCHAR(255) NOT NULL,
    image_url           TEXT,
    color               CHAR(7),
    sort_order          SMALLINT NOT NULL DEFAULT 0,
    is_active           BOOLEAN NOT NULL DEFAULT true,
    applicable_outlets  UUID[]
);

-- ── Products ──────────────────────────────────────────────────────────────
CREATE TABLE products (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    category_id      UUID NOT NULL REFERENCES categories(id),
    sku              VARCHAR(100),
    barcode          VARCHAR(100),
    name             VARCHAR(255) NOT NULL,
    description      TEXT,
    image_url        TEXT,
    product_type     VARCHAR(50) NOT NULL DEFAULT 'simple'
                     CHECK (product_type IN ('simple','variant','bundle','service','package')),
    base_price       DECIMAL(15,2) NOT NULL,
    cost_price       DECIMAL(15,2),
    unit             VARCHAR(50),
    track_stock      BOOLEAN NOT NULL DEFAULT false,
    has_modifiers    BOOLEAN NOT NULL DEFAULT false,
    preparation_time SMALLINT,
    kitchen_station  VARCHAR(100),
    is_available     BOOLEAN NOT NULL DEFAULT true,
    available_outlets UUID[],
    tags             TEXT[],
    metadata         JSONB,
    created_at       TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_products_sku     ON products(sku) WHERE sku IS NOT NULL;
CREATE UNIQUE INDEX idx_products_barcode ON products(barcode) WHERE barcode IS NOT NULL;
CREATE INDEX        idx_products_category ON products(category_id);
CREATE INDEX        idx_products_available ON products(is_available);
-- GIN index for tag search
CREATE INDEX        idx_products_tags ON products USING GIN(tags);

-- ── Outlet Stock ──────────────────────────────────────────────────────────
CREATE TABLE outlet_stock (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    outlet_id      UUID NOT NULL REFERENCES outlets(id),
    product_id     UUID NOT NULL REFERENCES products(id),
    variant_id     UUID,
    qty_on_hand    DECIMAL(12,3) NOT NULL DEFAULT 0,
    qty_reserved   DECIMAL(12,3) NOT NULL DEFAULT 0,
    qty_available  DECIMAL(12,3) GENERATED ALWAYS AS (qty_on_hand - qty_reserved) STORED,
    reorder_point  DECIMAL(12,3),
    reorder_qty    DECIMAL(12,3),
    last_counted_at TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(outlet_id, product_id, variant_id)
);

CREATE INDEX idx_outlet_stock_outlet   ON outlet_stock(outlet_id);
CREATE INDEX idx_outlet_stock_product  ON outlet_stock(product_id);

-- ── Stock Movements ───────────────────────────────────────────────────────
CREATE TABLE outlet_stock_movements (
    id             BIGSERIAL PRIMARY KEY,
    outlet_id      UUID NOT NULL REFERENCES outlets(id),
    product_id     UUID NOT NULL REFERENCES products(id),
    variant_id     UUID,
    movement_type  VARCHAR(50) NOT NULL
                   CHECK (movement_type IN ('sale','purchase','transfer_in','transfer_out','adjustment','opname','return','waste')),
    qty_change     DECIMAL(12,3) NOT NULL,
    qty_before     DECIMAL(12,3) NOT NULL,
    qty_after      DECIMAL(12,3) NOT NULL,
    reference_type VARCHAR(100),
    reference_id   UUID,
    notes          TEXT,
    created_by     UUID REFERENCES users(id),
    created_at     TIMESTAMP NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Create monthly partitions (example for 2024-2025)
CREATE TABLE outlet_stock_movements_2024_01 PARTITION OF outlet_stock_movements
    FOR VALUES FROM ('2024-01-01') TO ('2024-02-01');
CREATE TABLE outlet_stock_movements_2025_01 PARTITION OF outlet_stock_movements
    FOR VALUES FROM ('2025-01-01') TO ('2025-02-01');
-- Add more partitions as needed or use pg_partman

CREATE INDEX idx_stock_mov_outlet   ON outlet_stock_movements(outlet_id, created_at);
CREATE INDEX idx_stock_mov_product  ON outlet_stock_movements(product_id);
CREATE INDEX idx_stock_mov_ref      ON outlet_stock_movements(reference_type, reference_id);

-- ── Customers ─────────────────────────────────────────────────────────────
CREATE TABLE customers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                VARCHAR(100),
    full_name           VARCHAR(255) NOT NULL,
    phone               VARCHAR(30),
    email               VARCHAR(255),
    birthday            DATE,
    gender              CHAR(1),
    tier_id             UUID,
    points_balance      INTEGER NOT NULL DEFAULT 0,
    total_spend         DECIMAL(15,2) NOT NULL DEFAULT 0,
    visit_count         INTEGER NOT NULL DEFAULT 0,
    last_visit_at       TIMESTAMP,
    preferred_outlet_id UUID REFERENCES outlets(id),
    notes               TEXT,
    tags                TEXT[],
    extra_data          JSONB,
    is_active           BOOLEAN NOT NULL DEFAULT true,
    registered_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_customers_phone ON customers(phone) WHERE phone IS NOT NULL;
CREATE UNIQUE INDEX idx_customers_code  ON customers(code) WHERE code IS NOT NULL;
CREATE INDEX        idx_customers_tier  ON customers(tier_id);

-- ── Tables (F&B) ──────────────────────────────────────────────────────────
CREATE TABLE tables (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    outlet_id        UUID NOT NULL REFERENCES outlets(id),
    number           VARCHAR(20) NOT NULL,
    capacity         SMALLINT NOT NULL DEFAULT 4,
    shape            VARCHAR(20) DEFAULT 'square',
    pos_x            SMALLINT,
    pos_y            SMALLINT,
    status           VARCHAR(20) NOT NULL DEFAULT 'available'
                     CHECK (status IN ('available','occupied','reserved','cleaning','inactive')),
    current_order_id UUID,
    is_active        BOOLEAN NOT NULL DEFAULT true,
    UNIQUE(outlet_id, number)
);

CREATE INDEX idx_tables_outlet ON tables(outlet_id, status);

-- ── Orders ────────────────────────────────────────────────────────────────
CREATE TABLE orders (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_number     VARCHAR(50) UNIQUE NOT NULL,
    outlet_id        UUID NOT NULL REFERENCES outlets(id),
    terminal_id      UUID REFERENCES terminals(id),
    cashier_id       UUID NOT NULL REFERENCES users(id),
    customer_id      UUID REFERENCES customers(id),
    transaction_type VARCHAR(50) NOT NULL
                     CHECK (transaction_type IN ('dine_in','takeaway','delivery','walkin','service')),
    table_id         UUID REFERENCES tables(id),
    status           VARCHAR(20) NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','confirmed','in_progress','completed','cancelled','voided')),
    subtotal         DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_amount  DECIMAL(15,2) NOT NULL DEFAULT 0,
    tax_amount       DECIMAL(15,2) NOT NULL DEFAULT 0,
    service_charge   DECIMAL(15,2) NOT NULL DEFAULT 0,
    rounding         DECIMAL(15,2) NOT NULL DEFAULT 0,
    total_amount     DECIMAL(15,2) NOT NULL DEFAULT 0,
    notes            TEXT,
    flow_snapshot    JSONB,
    extra_data       JSONB,
    completed_at     TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

CREATE TABLE orders_2024 PARTITION OF orders FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');
CREATE TABLE orders_2025 PARTITION OF orders FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
CREATE TABLE orders_2026 PARTITION OF orders FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');

CREATE INDEX idx_orders_outlet      ON orders(outlet_id, created_at DESC);
CREATE INDEX idx_orders_cashier     ON orders(cashier_id);
CREATE INDEX idx_orders_status      ON orders(status);
CREATE INDEX idx_orders_customer    ON orders(customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX idx_orders_table       ON orders(table_id) WHERE table_id IS NOT NULL;

-- ── Order Items ───────────────────────────────────────────────────────────
CREATE TABLE order_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES orders(id),
    product_id      UUID NOT NULL REFERENCES products(id),
    variant_id      UUID,
    product_name    VARCHAR(255) NOT NULL,
    unit_price      DECIMAL(15,2) NOT NULL,
    quantity        DECIMAL(10,3) NOT NULL,
    modifiers       JSONB,         -- [{group, item, price}] — embedded, never queried alone
    discount_amount DECIMAL(15,2) NOT NULL DEFAULT 0,
    tax_amount      DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total      DECIMAL(15,2) NOT NULL,
    notes           TEXT,
    kitchen_status  VARCHAR(50),
    assigned_to     UUID REFERENCES users(id),
    status          VARCHAR(20) NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','voided','returned')),
    created_at      TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_order_items_order   ON order_items(order_id);
CREATE INDEX idx_order_items_product ON order_items(product_id);
CREATE INDEX idx_order_items_kitchen ON order_items(kitchen_status) WHERE kitchen_status IS NOT NULL;

-- ── Payments ──────────────────────────────────────────────────────────────
CREATE TABLE payments (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id         UUID NOT NULL REFERENCES orders(id),
    payment_method_id UUID NOT NULL,
    amount           DECIMAL(15,2) NOT NULL,
    tendered_amount  DECIMAL(15,2),
    change_amount    DECIMAL(15,2),
    reference_number VARCHAR(255),
    gateway_response JSONB,
    status           VARCHAR(20) NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','success','failed','refunded','voided')),
    processed_at     TIMESTAMP,
    processed_by     UUID NOT NULL REFERENCES users(id),
    created_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payments_order  ON payments(order_id);
CREATE INDEX idx_payments_status ON payments(status);

-- ── Audit Log ─────────────────────────────────────────────────────────────
CREATE TABLE audit_logs (
    id          BIGSERIAL,
    entity_type VARCHAR(100) NOT NULL,
    entity_id   UUID NOT NULL,
    action      VARCHAR(50) NOT NULL,
    user_id     UUID REFERENCES users(id),
    terminal_id UUID REFERENCES terminals(id),
    ip_address  INET,
    old_values  JSONB,
    new_values  JSONB,
    diff        JSONB,
    reason      TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

CREATE TABLE audit_logs_2024 PARTITION OF audit_logs FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');
CREATE TABLE audit_logs_2025 PARTITION OF audit_logs FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
CREATE TABLE audit_logs_2026 PARTITION OF audit_logs FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');

CREATE INDEX idx_audit_entity  ON audit_logs(entity_type, entity_id);
CREATE INDEX idx_audit_user    ON audit_logs(user_id, created_at DESC);

-- ── Outlet Config ─────────────────────────────────────────────────────────
CREATE TABLE outlet_config (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    outlet_id    UUID NOT NULL REFERENCES outlets(id),
    config_key   VARCHAR(200) NOT NULL,
    config_value JSONB NOT NULL,
    data_type    VARCHAR(50) NOT NULL DEFAULT 'string',
    updated_by   UUID REFERENCES users(id),
    updated_at   TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(outlet_id, config_key)
);

CREATE INDEX idx_outlet_config_outlet ON outlet_config(outlet_id);

-- ── Seed: Default roles ───────────────────────────────────────────────────
INSERT INTO roles (code, name, is_system, permissions) VALUES
('owner',      'Owner',      true, '{"*": true}'),
('manager',    'Manager',    true, '{"orders":{"create":true,"void":true,"view":true},"products":{"create":true,"update":true,"view":true},"reports":{"view":true},"stock":{"adjust":true}}'),
('cashier',    'Kasir',      true, '{"orders":{"create":true,"view":true},"products":{"view":true},"payments":{"process":true}}'),
('waiter',     'Pelayan',    true, '{"orders":{"create":true,"view":true},"tables":{"update":true}}'),
('kitchen',    'Dapur',      true, '{"kitchen_tickets":{"view":true,"update":true}}');
