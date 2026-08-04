package proxyconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

const (
	ModeDirect = "direct"
	ModeSystem = "system"
	ModeManual = "manual"
)

type Config struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}

type Status struct {
	Config          Config `json:"config"`
	EffectiveProxy  string `json:"effective_proxy,omitempty"`
	SystemAvailable bool   `json:"system_available"`
	SystemError     string `json:"system_error,omitempty"`
}

type Manager struct {
	config atomic.Value
	db     *sql.DB
}

func New(initial Config) (*Manager, error) {
	manager := &Manager{}
	manager.config.Store(Config{Mode: ModeDirect})
	if err := manager.Update(initial); err != nil {
		return nil, err
	}
	return manager, nil
}

func NewPersistent(db *sql.DB, fallback Config) (*Manager, error) {
	manager := &Manager{db: db}
	manager.config.Store(Config{Mode: ModeDirect})
	var raw string
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = 'proxy'`).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &fallback); err != nil {
			return nil, fmt.Errorf("decode saved proxy configuration: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load proxy configuration: %w", err)
	}
	normalized, err := normalize(fallback, false)
	if err != nil {
		return nil, err
	}
	manager.config.Store(normalized)
	return manager, nil
}

func (m *Manager) Config() Config {
	return m.config.Load().(Config)
}

func (m *Manager) Update(next Config) error {
	normalized, err := normalize(next, false)
	if err != nil {
		return err
	}
	m.config.Store(normalized)
	return nil
}

func (m *Manager) UpdatePersistent(ctx context.Context, next Config) error {
	normalized, err := normalize(next, true)
	if err != nil {
		return err
	}
	if m.db != nil {
		raw, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		_, err = m.db.ExecContext(ctx, `
			INSERT INTO app_settings(key, value, updated_at) VALUES ('proxy', ?, unixepoch())
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, string(raw))
		if err != nil {
			return fmt.Errorf("save proxy configuration: %w", err)
		}
	}
	m.config.Store(normalized)
	return nil
}

func normalize(next Config, requireSystemProxy bool) (Config, error) {
	next.Mode = strings.ToLower(strings.TrimSpace(next.Mode))
	next.URL = strings.TrimSpace(next.URL)
	if next.Mode == "" {
		next.Mode = ModeDirect
	}
	switch next.Mode {
	case ModeDirect:
		next.URL = ""
	case ModeManual:
		if _, err := parseProxyURL(next.URL); err != nil {
			return Config{}, err
		}
	case ModeSystem:
		next.URL = ""
		if _, err := systemProxyForURL(&url.URL{Scheme: "https", Host: "example.com"}); requireSystemProxy && err != nil {
			return Config{}, fmt.Errorf("read Windows system proxy: %w", err)
		}
	default:
		return Config{}, fmt.Errorf("unsupported proxy mode %q", next.Mode)
	}
	return next, nil
}

func (m *Manager) ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(request *http.Request) (*url.URL, error) {
		switch cfg := m.Config(); cfg.Mode {
		case ModeDirect:
			return nil, nil
		case ModeManual:
			return parseProxyURL(cfg.URL)
		case ModeSystem:
			return systemProxyForURL(request.URL)
		default:
			return nil, fmt.Errorf("unsupported proxy mode %q", cfg.Mode)
		}
	}
}

func (m *Manager) Status() Status {
	status := Status{Config: m.Config()}
	systemProxy, err := systemProxyForURL(&url.URL{Scheme: "https", Host: "example.com"})
	if err != nil {
		status.SystemError = err.Error()
	} else if systemProxy != nil {
		status.SystemAvailable = true
		status.EffectiveProxy = redactProxy(systemProxy)
	}
	if status.Config.Mode == ModeManual {
		if parsed, err := parseProxyURL(status.Config.URL); err == nil {
			status.EffectiveProxy = redactProxy(parsed)
		}
	}
	if status.Config.Mode == ModeDirect {
		status.EffectiveProxy = ""
	}
	return status
}

func parseProxyURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("manual proxy URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("proxy URL scheme must be http, https, socks5, or socks5h")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL must include a host")
	}
	return parsed, nil
}

func redactProxy(proxyURL *url.URL) string {
	copy := *proxyURL
	if copy.User != nil {
		copy.User = url.UserPassword(copy.User.Username(), "xxxxx")
	}
	return copy.String()
}
