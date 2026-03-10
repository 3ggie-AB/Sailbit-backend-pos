package main

import (
	"strings"
	"time"

	"github.com/spf13/viper"
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
	TenantMaxConn   int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	PoolSize int

	SessionTTL    time.Duration
	ConfigTTL     time.Duration
	TenantInfoTTL time.Duration
}

type JWTConfig struct {
	AccessSecret  string
	RefreshSecret string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

type SSEConfig struct {
	HeartbeatInterval time.Duration
	BufferSize        int
}

func loadConfig() (*Config, error) {
	v := viper.New()

	v.SetDefault("app.env", "development")
	v.SetDefault("app.addr", ":8080")
	v.SetDefault("app.version", "1.0.0")

	v.SetDefault("db.platform_max_conn", 25)
	v.SetDefault("db.tenant_max_conn", 50)
	v.SetDefault("db.max_conn_lifetime", "1h")
	v.SetDefault("db.max_conn_idle_time", "30m")

	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.pool_size", 20)
	v.SetDefault("redis.session_ttl", "24h")
	v.SetDefault("redis.config_ttl", "5m")
	v.SetDefault("redis.tenant_info_ttl", "10m")

	v.SetDefault("jwt.access_ttl", "15m")
	v.SetDefault("jwt.refresh_ttl", "168h")

	v.SetDefault("sse.heartbeat_interval", "30s")
	v.SetDefault("sse.buffer_size", 256)

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")

	v.SetEnvPrefix("SAILBIT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	_ = v.ReadInConfig()

	return &Config{
		App: AppConfig{
			Env:     v.GetString("app.env"),
			Addr:    v.GetString("app.addr"),
			Version: v.GetString("app.version"),
		},
		DB: DBConfig{
			PlatformDSN:     v.GetString("db.platform_dsn"),
			TenantDSN:       v.GetString("db.tenant_dsn"),
			PlatformMaxConn: int32(v.GetInt("db.platform_max_conn")),
			TenantMaxConn:   int32(v.GetInt("db.tenant_max_conn")),
			MaxConnLifetime: v.GetDuration("db.max_conn_lifetime"),
			MaxConnIdleTime: v.GetDuration("db.max_conn_idle_time"),
		},
		Redis: RedisConfig{
			Addr:          v.GetString("redis.addr"),
			Password:      v.GetString("redis.password"),
			DB:            v.GetInt("redis.db"),
			PoolSize:      v.GetInt("redis.pool_size"),
			SessionTTL:    v.GetDuration("redis.session_ttl"),
			ConfigTTL:     v.GetDuration("redis.config_ttl"),
			TenantInfoTTL: v.GetDuration("redis.tenant_info_ttl"),
		},
		JWT: JWTConfig{
			AccessSecret:  v.GetString("jwt.access_secret"),
			RefreshSecret: v.GetString("jwt.refresh_secret"),
			AccessTTL:     v.GetDuration("jwt.access_ttl"),
			RefreshTTL:    v.GetDuration("jwt.refresh_ttl"),
		},
		SSE: SSEConfig{
			HeartbeatInterval: v.GetDuration("sse.heartbeat_interval"),
			BufferSize:        v.GetInt("sse.buffer_size"),
		},
	}, nil
}