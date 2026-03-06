package lplexc

import (
	"log/slog"
	"net/http"
	"time"
)

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

type clientConfig struct {
	httpClient       *http.Client
	logger           *slog.Logger
	reconnectBackoff BackoffConfig
}

// BackoffConfig controls exponential backoff for auto-reconnect.
type BackoffConfig struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxRetries      int // 0 means unlimited
}

var defaultBackoff = BackoffConfig{
	InitialInterval: 1 * time.Second,
	MaxInterval:     30 * time.Second,
	MaxRetries:      0,
}

func defaultConfig() clientConfig {
	return clientConfig{
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		logger:           slog.Default(),
		reconnectBackoff: defaultBackoff,
	}
}

// WithHTTPClient sets a custom http.Client for all requests.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = c
	}
}

// WithTransport sets a custom http.Transport on the default http.Client.
// This is a convenience for configuring connection pooling parameters.
func WithTransport(t http.RoundTripper) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = &http.Client{Transport: t}
	}
}

// WithPoolSize sets the maximum number of idle connections per host.
func WithPoolSize(n int) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        n,
				MaxIdleConnsPerHost: n,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
}

// WithLogger sets a structured logger for the client.
func WithLogger(l *slog.Logger) ClientOption {
	return func(cfg *clientConfig) {
		cfg.logger = l
	}
}

// WithBackoff configures the reconnection backoff strategy.
func WithBackoff(b BackoffConfig) ClientOption {
	return func(cfg *clientConfig) {
		cfg.reconnectBackoff = b
	}
}
