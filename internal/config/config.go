package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr             string
	GRPCAddr             string
	PublicWSBaseURL      *url.URL
	AllowedOrigins       map[string]struct{}
	StoreMode            string
	RedisURL             string
	TicketKeyFile        string
	TicketKey            [32]byte
	TicketTTL            time.Duration
	SessionMaxDuration   time.Duration
	IdempotencyTTL       time.Duration
	MaxActiveSessions    int
	MaxActivePerSubject  int
	WSMaxMessageBytes    int64
	WSHandshakeTimeout   time.Duration
	WSIdleTimeout        time.Duration
	ShutdownGracePeriod  time.Duration
	OTelServiceName      string
	OTelExporterEndpoint string
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:             env("HTTP_ADDR", ":8080"),
		GRPCAddr:             env("GRPC_ADDR", ":9090"),
		StoreMode:            env("STORE_MODE", "redis"),
		RedisURL:             os.Getenv("REDIS_URL"),
		TicketKeyFile:        os.Getenv("TICKET_ENCRYPTION_KEY_FILE"),
		TicketTTL:            60 * time.Second,
		SessionMaxDuration:   15 * time.Minute,
		IdempotencyTTL:       15 * time.Minute,
		MaxActiveSessions:    100,
		MaxActivePerSubject:  5,
		WSMaxMessageBytes:    65536,
		WSHandshakeTimeout:   10 * time.Second,
		WSIdleTimeout:        10 * time.Minute,
		ShutdownGracePeriod:  25 * time.Second,
		OTelServiceName:      env("OTEL_SERVICE_NAME", "ani-session-gateway"),
		OTelExporterEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	}
	var err error
	if c.PublicWSBaseURL, err = parsePublicURL(os.Getenv("PUBLIC_WS_BASE_URL")); err != nil {
		return Config{}, err
	}
	if c.AllowedOrigins, err = parseOrigins(os.Getenv("ALLOWED_ORIGINS")); err != nil {
		return Config{}, err
	}
	if c.TicketKeyFile == "" {
		return Config{}, errors.New("TICKET_ENCRYPTION_KEY_FILE is required")
	}
	key, err := os.ReadFile(c.TicketKeyFile)
	if err != nil {
		return Config{}, fmt.Errorf("read TICKET_ENCRYPTION_KEY_FILE: %w", err)
	}
	if len(key) != len(c.TicketKey) {
		return Config{}, errors.New("TICKET_ENCRYPTION_KEY_FILE must contain exactly 32 raw bytes")
	}
	copy(c.TicketKey[:], key)
	if err = validateAddr("HTTP_ADDR", c.HTTPAddr); err != nil {
		return Config{}, err
	}
	if err = validateAddr("GRPC_ADDR", c.GRPCAddr); err != nil {
		return Config{}, err
	}
	if c.HTTPAddr == c.GRPCAddr {
		return Config{}, errors.New("HTTP_ADDR and GRPC_ADDR must differ")
	}
	if c.StoreMode != "memory" && c.StoreMode != "redis" {
		return Config{}, errors.New("STORE_MODE must be redis or memory")
	}
	if c.StoreMode == "redis" && c.RedisURL == "" {
		return Config{}, errors.New("REDIS_URL is required in redis mode")
	}
	if c.OTelServiceName == "" {
		return Config{}, errors.New("OTEL_SERVICE_NAME must not be empty")
	}
	if c.OTelExporterEndpoint != "" {
		u, err := url.Parse(c.OTelExporterEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT must be an http/https URL without userinfo, query, or fragment")
		}
	}
	for name, dst := range map[string]*time.Duration{
		"TICKET_TTL": &c.TicketTTL, "SESSION_MAX_DURATION": &c.SessionMaxDuration,
		"IDEMPOTENCY_TTL": &c.IdempotencyTTL, "WS_HANDSHAKE_TIMEOUT": &c.WSHandshakeTimeout,
		"WS_IDLE_TIMEOUT": &c.WSIdleTimeout, "SHUTDOWN_GRACE_PERIOD": &c.ShutdownGracePeriod,
	} {
		if *dst, err = duration(name, *dst); err != nil {
			return Config{}, err
		}
	}
	for name, dst := range map[string]*int{
		"MAX_ACTIVE_SESSIONS": &c.MaxActiveSessions, "MAX_ACTIVE_PER_SUBJECT": &c.MaxActivePerSubject,
	} {
		if *dst, err = positiveInt(name, *dst); err != nil {
			return Config{}, err
		}
	}
	if n, e := positiveInt("WS_MAX_MESSAGE_BYTES", int(c.WSMaxMessageBytes)); e != nil {
		return Config{}, e
	} else {
		c.WSMaxMessageBytes = int64(n)
	}
	if c.TicketTTL >= c.SessionMaxDuration {
		return Config{}, errors.New("TICKET_TTL must be shorter than SESSION_MAX_DURATION")
	}
	for origin := range c.AllowedOrigins {
		if strings.HasPrefix(origin, "https://") && c.PublicWSBaseURL.Scheme != "wss" {
			return Config{}, errors.New("HTTPS origins require a wss PUBLIC_WS_BASE_URL")
		}
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parsePublicURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("PUBLIC_WS_BASE_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("PUBLIC_WS_BASE_URL must be a ws/wss URL without userinfo, query, or fragment")
	}
	if strings.TrimSuffix(u.Path, "/") != "/api/v1/realtime" {
		return nil, errors.New("PUBLIC_WS_BASE_URL path must end with /api/v1/realtime")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func parseOrigins(raw string) (map[string]struct{}, error) {
	if raw == "" {
		return nil, errors.New("ALLOWED_ORIGINS is required")
	}
	result := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(item)
		if origin == "" || origin == "*" {
			return nil, errors.New("ALLOWED_ORIGINS must contain explicit origins")
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("invalid origin in ALLOWED_ORIGINS")
		}
		result[strings.TrimSuffix(origin, "/")] = struct{}{}
	}
	return result, nil
}

func validateAddr(name, value string) error {
	if _, _, err := net.SplitHostPort(value); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
func duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}
func positiveInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
