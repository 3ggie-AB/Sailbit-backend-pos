package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	App   AppConfig
	DB    DBConfig
	Redis RedisConfig
	JWT   JWTConfig
	SSE   SSEConfig
}

type AppConfig struct {
	Env     string
	Addr    string
	Version string
}

type DBConfig struct {
	PlatformDSN     string
	TenantDSN       string
	PlatformMaxConn int32
	PlatformMinConn int32
	TenantMaxConn   int32
	TenantMinConn   int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type RedisConfig struct {
	Addr           string
	Password       string
	DB             int
	PoolSize       int
	SessionTTL     time.Duration
	TenantInfoTTL  time.Duration
	ConfigTTL      time.Duration
}

type JWTConfig struct {
	AccessSecret  string
	RefreshSecret string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

type SSEConfig struct {
	BufferSize        int
	HeartbeatInterval time.Duration
}

func Load() (*Config, error) {
	return &Config{
		App: AppConfig{
			Env:     getEnv("POS_APP_ENV", "development"),
			Addr:    getEnv("POS_APP_ADDR", ":8080"),
			Version: getEnv("POS_APP_VERSION", "1.0.0"),
		},
		DB: DBConfig{
			PlatformDSN:     getEnv("POS_DB_PLATFORM_DSN", "postgres://pos:pos_secret@localhost:5432/pos_platform?sslmode=disable"),
			TenantDSN:       getEnv("POS_DB_TENANT_DSN", "postgres://pos:pos_secret@localhost:5432/pos_platform?sslmode=disable"),
			PlatformMaxConn: int32(getEnvInt("POS_DB_PLATFORM_MAX_CONN", 20)),
			PlatformMinConn: int32(getEnvInt("POS_DB_PLATFORM_MIN_CONN", 2)),
			TenantMaxConn:   int32(getEnvInt("POS_DB_TENANT_MAX_CONN", 10)),
			TenantMinConn:   int32(getEnvInt("POS_DB_TENANT_MIN_CONN", 1)),
			MaxConnLifetime: 30 * time.Minute,
			MaxConnIdleTime: 5 * time.Minute,
		},
		Redis: RedisConfig{
			Addr:          getEnv("POS_REDIS_ADDR", "localhost:6379"),
			Password:      getEnv("POS_REDIS_PASSWORD", ""),
			DB:            getEnvInt("POS_REDIS_DB", 0),
			PoolSize:      getEnvInt("POS_REDIS_POOL_SIZE", 20),
			SessionTTL:    15 * time.Minute,
			TenantInfoTTL: 10 * time.Minute,
			ConfigTTL:     5 * time.Minute,
		},
		JWT: JWTConfig{
			AccessSecret:  getEnv("POS_JWT_ACCESS_SECRET", "change_me_access_secret"),
			RefreshSecret: getEnv("POS_JWT_REFRESH_SECRET", "change_me_refresh_secret"),
			AccessTTL:     15 * time.Minute,
			RefreshTTL:    7 * 24 * time.Hour,
		},
		SSE: SSEConfig{
			BufferSize:        getEnvInt("POS_SSE_BUFFER_SIZE", 64),
			HeartbeatInterval: 30 * time.Second,
		},
	}, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}