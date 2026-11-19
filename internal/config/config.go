// Package config reads the process configuration from the environment.
//
// The storage and business layers never read environment variables directly;
// this keeps the core reusable outside the LazyCat microserver package.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the immutable process configuration.
type Config struct {
	Addr                string
	DataDir             string
	WebDir              string
	UpdateInterval      time.Duration
	HTTPTimeout         time.Duration
	MaxFeedBytes        int64
	UserHeader          string
	TrustUserHeader     bool
	AllowPrivateNetwork bool
	AllowFakeIPDNS      bool
	AllowInsecureAI     bool
	ITunesBase          string
	Secret              string
}

// Load reads configuration. It only validates values that would otherwise
// cause a confusing runtime failure.
func Load() (Config, error) {
	c := Config{
		Addr:                env("BOAI_ADDR", "127.0.0.1:8080"),
		DataDir:             env("BOAI_DATA_DIR", "./data"),
		WebDir:              env("BOAI_WEB_DIR", "./web/dist"),
		UpdateInterval:      envDuration("BOAI_UPDATE_INTERVAL", 30*time.Minute),
		HTTPTimeout:         envDuration("BOAI_HTTP_TIMEOUT", 30*time.Second),
		MaxFeedBytes:        envInt64("BOAI_MAX_FEED_BYTES", 16<<20),
		UserHeader:          env("BOAI_USER_HEADER", "X-HC-User-ID"),
		TrustUserHeader:     envBool("BOAI_TRUST_USER_HEADER", false),
		AllowPrivateNetwork: envBool("BOAI_ALLOW_PRIVATE_NETWORK", false),
		AllowFakeIPDNS:      envBool("BOAI_ALLOW_FAKE_IP_DNS", false),
		AllowInsecureAI:     envBool("BOAI_ALLOW_INSECURE_AI", false),
		ITunesBase:          env("BOAI_ITUNES_BASE", "https://itunes.apple.com"),
		Secret:              os.Getenv("BOAI_SECRET"),
	}
	if c.Addr == "" {
		return Config{}, fmt.Errorf("BOAI_ADDR must not be empty")
	}
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("BOAI_DATA_DIR must not be empty")
	}
	if c.UpdateInterval < time.Minute {
		return Config{}, fmt.Errorf("BOAI_UPDATE_INTERVAL must be at least 1m")
	}
	if c.HTTPTimeout < 5*time.Second {
		return Config{}, fmt.Errorf("BOAI_HTTP_TIMEOUT must be at least 5s")
	}
	if c.MaxFeedBytes < 64<<10 {
		return Config{}, fmt.Errorf("BOAI_MAX_FEED_BYTES must be at least 64 KiB")
	}
	if c.UserHeader == "" {
		c.UserHeader = "X-HC-User-ID"
	}
	u, err := url.Parse(c.ITunesBase)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Config{}, fmt.Errorf("BOAI_ITUNES_BASE must be an absolute HTTP(S) URL")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return d
}

func envInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
