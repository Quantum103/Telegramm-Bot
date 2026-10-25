package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config содержит настройки HTTP-сервера, Telegram-бота и подключения к PostgreSQL.
type Config struct {
	HTTPAddr              string
	HTTPReadTimeout       time.Duration
	HTTPReadHeaderTimeout time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	TelegramBotToken      string
	Database              DatabaseConfig
	Redis                 RedisConfig
}

type RedisConfig struct {
	Addr string
	Password string
	DB int
	ConnectTimeout time.Duration
}

// DatabaseConfig содержит параметры пула PostgreSQL.
type DatabaseConfig struct {
	Host           string
	Port           uint16
	Name           string
	User           string
	Password       string
	SSLMode        string
	ConnectTimeout time.Duration
	MaxConns       int32
	MinConns       int32
}

// Load читает настройки из переменных окружения и проверяет их согласованность.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	var cfg Config
	var err error

	cfg.HTTPAddr = stringValue(lookup, "HTTP_ADDR", ":8080")
	if err := validateListenAddress(cfg.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR: %w", err)
	}

	if cfg.HTTPReadTimeout, err = durationValue(lookup, "HTTP_READ_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPReadHeaderTimeout, err = durationValue(lookup, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPWriteTimeout, err = durationValue(lookup, "HTTP_WRITE_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPIdleTimeout, err = durationValue(lookup, "HTTP_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return Config{}, err
	}

	cfg.TelegramBotToken = strings.TrimSpace(stringValue(lookup, "TELEGRAM_BOT_TOKEN", ""))

	cfg.Redis.Addr = strings.TrimSpace(stringValue(lookup, "REDIS_ADDR", "localhost:6379"))
	host, redisPort, redisAddrErr := net.SplitHostPort(cfg.Redis.Addr)
	parsedRedisPort, parsedRedisPortErr := strconv.Atoi(redisPort)
	if redisAddrErr != nil || host == "" || parsedRedisPortErr != nil || parsedRedisPort < 1 || parsedRedisPort > 65535 {
		return Config{}, fmt.Errorf("REDIS_ADDR must be a valid host:port address")
	}
	cfg.Redis.Password = stringValue(lookup, "REDIS_PASSWORD", "")
	if strings.TrimSpace(cfg.Redis.Password) == "" { return Config{}, fmt.Errorf("REDIS_PASSWORD is required") }
	redisDB, err := intValue(lookup, "REDIS_DB", 0)
	if err != nil { return Config{}, err }
	if redisDB < 0 || redisDB > 15 { return Config{}, fmt.Errorf("REDIS_DB must be between 0 and 15") }
	cfg.Redis.DB = redisDB
	if cfg.Redis.ConnectTimeout, err = durationValue(lookup, "REDIS_CONNECT_TIMEOUT", 3*time.Second); err != nil { return Config{}, err }

	cfg.Database.Host = strings.TrimSpace(stringValue(lookup, "DATABASE_HOST", ""))
	cfg.Database.Name = strings.TrimSpace(stringValue(lookup, "DATABASE_NAME", ""))
	cfg.Database.User = strings.TrimSpace(stringValue(lookup, "DATABASE_USER", ""))
	cfg.Database.Password = stringValue(lookup, "DATABASE_PASSWORD", "")
	if cfg.Database.Host == "" {
		return Config{}, fmt.Errorf("DATABASE_HOST is required")
	}
	if cfg.Database.Name == "" {
		return Config{}, fmt.Errorf("DATABASE_NAME is required")
	}
	if cfg.Database.User == "" {
		return Config{}, fmt.Errorf("DATABASE_USER is required")
	}
	if strings.TrimSpace(cfg.Database.Password) == "" {
		return Config{}, fmt.Errorf("DATABASE_PASSWORD is required")
	}

	port, err := intValue(lookup, "DATABASE_PORT", 5432)
	if err != nil {
		return Config{}, err
	}
	if port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("DATABASE_PORT must be between 1 and 65535")
	}
	cfg.Database.Port = uint16(port)

	cfg.Database.SSLMode = stringValue(lookup, "DATABASE_SSLMODE", "disable")
	switch cfg.Database.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return Config{}, fmt.Errorf("DATABASE_SSLMODE must be one of disable, allow, prefer, require, verify-ca, verify-full")
	}

	if cfg.Database.ConnectTimeout, err = durationValue(lookup, "DATABASE_CONNECT_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}

	maxConns, err := intValue(lookup, "DATABASE_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	minConns, err := intValue(lookup, "DATABASE_MIN_CONNS", 1)
	if err != nil {
		return Config{}, err
	}
	if maxConns < 1 || maxConns > 1000 {
		return Config{}, fmt.Errorf("DATABASE_MAX_CONNS must be between 1 and 1000")
	}
	if minConns < 0 || minConns > maxConns {
		return Config{}, fmt.Errorf("DATABASE_MIN_CONNS must be between 0 and DATABASE_MAX_CONNS")
	}
	cfg.Database.MaxConns = int32(maxConns)
	cfg.Database.MinConns = int32(minConns)

	return cfg, nil
}

func stringValue(lookup func(string) (string, bool), key, fallback string) string {
	value, ok := lookup(key)
	if !ok {
		return fallback
	}
	return value
}

func durationValue(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(stringValue(lookup, key, ""))
	if value == "" {
		return fallback, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 5s or 1m", key)
	}
	return duration, nil
}

func intValue(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	value := strings.TrimSpace(stringValue(lookup, key, ""))
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func validateListenAddress(address string) error {
	if strings.TrimSpace(address) != address || address == "" {
		return fmt.Errorf("must be a valid host:port address")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("must be a valid host:port address, for example :8080")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
