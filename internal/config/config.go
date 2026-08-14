package config

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxBodyBytes = 64 << 20
	DefaultConfigPath   = "data/config.json"
)

type Config struct {
	Server   ServerConfig     `json:"server"`
	Accounts []Account        `json:"accounts"`
	Routes   map[string]Route `json:"routes"`
}

type ServerConfig struct {
	Listen                   string   `json:"listen"`
	APIKeys                  []string `json:"api_keys,omitempty"`
	APIKeyEnv                string   `json:"api_key_env,omitempty"`
	AdminToken               string   `json:"admin_token,omitempty"`
	AdminTokenEnv            string   `json:"admin_token_env,omitempty"`
	ClientKeysPath           string   `json:"client_keys_path,omitempty"`
	AdminAllowedCIDRs        []string `json:"admin_allowed_cidrs,omitempty"`
	TrustedProxyCIDRs        []string `json:"trusted_proxy_cidrs,omitempty"`
	AdminSessionTTL          Duration `json:"admin_session_ttl,omitempty"`
	MaxBodyBytes             int64    `json:"max_body_bytes"`
	MaxInFlightRequests      int      `json:"max_inflight_requests"`
	RequestReadTimeout       Duration `json:"request_read_timeout"`
	QueueTimeout             Duration `json:"queue_timeout"`
	ResponseHeaderTimeout    Duration `json:"response_header_timeout"`
	StreamIdleTimeout        Duration `json:"stream_idle_timeout"`
	IdleConnTimeout          Duration `json:"idle_conn_timeout"`
	MaxIdleConns             int      `json:"max_idle_conns"`
	MaxIdleConnsPerHost      int      `json:"max_idle_conns_per_host"`
	MaxConnsPerHost          int      `json:"max_conns_per_host"`
	FailureThreshold         int      `json:"failure_threshold"`
	CircuitCooldown          Duration `json:"circuit_cooldown"`
	MaxFailoverAttempts      int      `json:"max_failover_attempts"`
	AllowPrivateHTTPUpstream bool     `json:"allow_private_http_upstream"`
}

type Account struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Type        string            `json:"type"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key,omitempty"`
	APIKeyEnv   string            `json:"api_key_env,omitempty"`
	AuthHeader  string            `json:"auth_header,omitempty"`
	AuthScheme  string            `json:"auth_scheme,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	HeadersEnv  map[string]string `json:"headers_env,omitempty"`
	Models      []string          `json:"models,omitempty"`
	ModelMap    map[string]string `json:"model_map,omitempty"`
	Priority    int               `json:"priority"`
	Weight      int               `json:"weight"`
	Concurrency int               `json:"concurrency"`
	Enabled     bool              `json:"enabled"`
	ProxyURL    string            `json:"proxy_url,omitempty"`
}

type Route struct {
	Accounts      []string `json:"accounts,omitempty"`
	UpstreamModel string   `json:"upstream_model,omitempty"`
	Strategy      string   `json:"strategy,omitempty"`
}

type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		v, err := time.ParseDuration(text)
		if err != nil {
			return err
		}
		d.Duration = v
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return errors.New("duration must be a duration string such as 30s")
	}
	d.Duration = time.Duration(nanos)
	return nil
}

func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Listen:                "127.0.0.1:45679",
			APIKeyEnv:             "LITE2API_API_KEYS",
			AdminTokenEnv:         "LITE2API_ADMIN_TOKEN",
			ClientKeysPath:        "client_keys.json",
			AdminAllowedCIDRs:     []string{"127.0.0.0/8", "::1/128"},
			TrustedProxyCIDRs:     []string{"127.0.0.0/8", "::1/128"},
			AdminSessionTTL:       Duration{30 * time.Minute},
			MaxBodyBytes:          DefaultMaxBodyBytes,
			MaxInFlightRequests:   256,
			RequestReadTimeout:    Duration{30 * time.Second},
			QueueTimeout:          Duration{30 * time.Second},
			ResponseHeaderTimeout: Duration{5 * time.Minute},
			StreamIdleTimeout:     Duration{15 * time.Minute},
			IdleConnTimeout:       Duration{90 * time.Second},
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   128,
			MaxConnsPerHost:       256,
			FailureThreshold:      3,
			CircuitCooldown:       Duration{30 * time.Second},
			MaxFailoverAttempts:   3,
		},
		Routes: make(map[string]Route),
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg = Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Normalize(cfg Config) Config {
	cfg.Accounts = append([]Account(nil), cfg.Accounts...)
	applyDefaults(&cfg)
	return cfg
}

func applyDefaults(cfg *Config) {
	d := Defaults().Server
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = d.Listen
	}
	if cfg.Server.APIKeyEnv == "" {
		cfg.Server.APIKeyEnv = d.APIKeyEnv
	}
	if cfg.Server.AdminTokenEnv == "" {
		cfg.Server.AdminTokenEnv = d.AdminTokenEnv
	}
	if cfg.Server.ClientKeysPath == "" {
		cfg.Server.ClientKeysPath = d.ClientKeysPath
	}
	if len(cfg.Server.AdminAllowedCIDRs) == 0 {
		cfg.Server.AdminAllowedCIDRs = append([]string(nil), d.AdminAllowedCIDRs...)
	}
	if value := strings.TrimSpace(os.Getenv("LITE2API_ADMIN_ALLOWED_CIDRS")); value != "" {
		cfg.Server.AdminAllowedCIDRs = splitCommaList(value)
	}
	if len(cfg.Server.TrustedProxyCIDRs) == 0 {
		cfg.Server.TrustedProxyCIDRs = append([]string(nil), d.TrustedProxyCIDRs...)
	}
	if value := strings.TrimSpace(os.Getenv("LITE2API_TRUSTED_PROXY_CIDRS")); value != "" {
		cfg.Server.TrustedProxyCIDRs = splitCommaList(value)
	}
	if cfg.Server.AdminSessionTTL.Duration == 0 {
		cfg.Server.AdminSessionTTL = d.AdminSessionTTL
	}
	if cfg.Server.MaxBodyBytes <= 0 {
		cfg.Server.MaxBodyBytes = d.MaxBodyBytes
	}
	applyPositiveInt64Env(&cfg.Server.MaxBodyBytes, "LITE2API_MAX_BODY_BYTES")
	if cfg.Server.MaxInFlightRequests <= 0 {
		cfg.Server.MaxInFlightRequests = d.MaxInFlightRequests
	}
	applyPositiveIntEnv(&cfg.Server.MaxInFlightRequests, "LITE2API_MAX_INFLIGHT_REQUESTS")
	if cfg.Server.RequestReadTimeout.Duration <= 0 {
		cfg.Server.RequestReadTimeout = d.RequestReadTimeout
	}
	// QueueTimeout intentionally keeps an explicit zero value (fail fast).
	if cfg.Server.ResponseHeaderTimeout.Duration == 0 {
		cfg.Server.ResponseHeaderTimeout = d.ResponseHeaderTimeout
	}
	if cfg.Server.StreamIdleTimeout.Duration == 0 {
		cfg.Server.StreamIdleTimeout = d.StreamIdleTimeout
	}
	if cfg.Server.IdleConnTimeout.Duration == 0 {
		cfg.Server.IdleConnTimeout = d.IdleConnTimeout
	}
	if cfg.Server.MaxIdleConns <= 0 {
		cfg.Server.MaxIdleConns = d.MaxIdleConns
	}
	applyPositiveIntEnv(&cfg.Server.MaxIdleConns, "LITE2API_MAX_IDLE_CONNS")
	if cfg.Server.MaxIdleConnsPerHost <= 0 {
		cfg.Server.MaxIdleConnsPerHost = d.MaxIdleConnsPerHost
	}
	applyPositiveIntEnv(&cfg.Server.MaxIdleConnsPerHost, "LITE2API_MAX_IDLE_CONNS_PER_HOST")
	if cfg.Server.MaxConnsPerHost <= 0 {
		cfg.Server.MaxConnsPerHost = d.MaxConnsPerHost
	}
	applyPositiveIntEnv(&cfg.Server.MaxConnsPerHost, "LITE2API_MAX_CONNS_PER_HOST")
	if cfg.Server.FailureThreshold <= 0 {
		cfg.Server.FailureThreshold = d.FailureThreshold
	}
	if cfg.Server.CircuitCooldown.Duration <= 0 {
		cfg.Server.CircuitCooldown = d.CircuitCooldown
	}
	if cfg.Server.MaxFailoverAttempts <= 0 {
		cfg.Server.MaxFailoverAttempts = d.MaxFailoverAttempts
	}
	if cfg.Routes == nil {
		cfg.Routes = make(map[string]Route)
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Type == "" {
			a.Type = "openai"
		}
		if a.AuthHeader == "" {
			if a.Type == "anthropic" {
				a.AuthHeader = "x-api-key"
			} else {
				a.AuthHeader = "authorization"
			}
		}
		if a.AuthHeader == "authorization" && a.AuthScheme == "" {
			a.AuthScheme = "Bearer"
		}
		if a.Weight <= 0 {
			a.Weight = 1
		}
	}
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func applyPositiveIntEnv(target *int, name string) {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && value > 0 {
		*target = value
	}
}

func applyPositiveInt64Env(target *int64, name string) {
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64); err == nil && value > 0 {
		*target = value
	}
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	if c.Server.AdminSessionTTL.Duration < 5*time.Minute || c.Server.AdminSessionTTL.Duration > 24*time.Hour {
		return errors.New("server.admin_session_ttl must be between 5m and 24h")
	}
	if filepath.IsAbs(c.Server.ClientKeysPath) && filepath.Clean(c.Server.ClientKeysPath) == string(filepath.Separator) {
		return errors.New("server.client_keys_path cannot be the filesystem root")
	}
	for field, values := range map[string][]string{
		"server.admin_allowed_cidrs": c.Server.AdminAllowedCIDRs,
		"server.trusted_proxy_cidrs": c.Server.TrustedProxyCIDRs,
	} {
		for _, value := range values {
			if _, _, err := net.ParseCIDR(value); err != nil {
				return fmt.Errorf("%s contains invalid CIDR %q", field, value)
			}
		}
	}
	seen := make(map[string]struct{}, len(c.Accounts))
	for _, a := range c.Accounts {
		if a.ID == "" {
			return errors.New("account id is required")
		}
		if _, ok := seen[a.ID]; ok {
			return fmt.Errorf("duplicate account id %q", a.ID)
		}
		seen[a.ID] = struct{}{}
		if a.Concurrency < 0 {
			return fmt.Errorf("account %q concurrency cannot be negative", a.ID)
		}
		if a.Type != "openai" && a.Type != "anthropic" {
			return fmt.Errorf("account %q: unsupported type %q", a.ID, a.Type)
		}
		if a.AuthHeader != "" && !validHeaderName(a.AuthHeader) && a.AuthHeader != "none" {
			return fmt.Errorf("account %q has invalid auth_header", a.ID)
		}
		for name := range a.Headers {
			if !validHeaderName(name) {
				return fmt.Errorf("account %q has invalid header name %q", a.ID, name)
			}
		}
		for name := range a.HeadersEnv {
			if !validHeaderName(name) {
				return fmt.Errorf("account %q has invalid environment header name %q", a.ID, name)
			}
		}
		if err := validateURL(a.BaseURL, c.Server.AllowPrivateHTTPUpstream); err != nil {
			return fmt.Errorf("account %q base_url: %w", a.ID, err)
		}
		if a.ProxyURL != "" {
			u, err := url.Parse(a.ProxyURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
				return fmt.Errorf("account %q has invalid proxy_url", a.ID)
			}
		}
	}
	for model, route := range c.Routes {
		if strings.TrimSpace(model) == "" {
			return errors.New("route model cannot be empty")
		}
		if route.Strategy != "" && route.Strategy != "least_loaded" && route.Strategy != "round_robin" && route.Strategy != "priority" && route.Strategy != "sticky" {
			return fmt.Errorf("route %q has unsupported strategy %q", model, route.Strategy)
		}
		for _, id := range route.Accounts {
			if _, ok := seen[id]; !ok {
				return fmt.Errorf("route %q references unknown account %q", model, id)
			}
		}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}

func validateURL(raw string, allowPrivateHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if u.User != nil || u.Fragment != "" {
		return errors.New("credentials and fragments are forbidden")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("scheme must be http or https")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
		private := (ip != nil && ip.IsPrivate()) || !strings.Contains(host, ".")
		if !loopback && (!allowPrivateHTTP || !private) {
			return errors.New("plain HTTP is allowed only for loopback or explicitly enabled private hosts")
		}
	}
	return nil
}

func (c Config) GatewayKeys() []string {
	keys := append([]string(nil), c.Server.APIKeys...)
	if env := strings.TrimSpace(os.Getenv(c.Server.APIKeyEnv)); env != "" {
		for _, key := range strings.Split(env, ",") {
			if key = strings.TrimSpace(key); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return slices.Compact(keys)
}

func (c Config) ResolvedAdminToken() string {
	if c.Server.AdminTokenEnv != "" {
		if token := strings.TrimSpace(os.Getenv(c.Server.AdminTokenEnv)); token != "" {
			return token
		}
	}
	return c.Server.AdminToken
}

func (a Account) ResolvedAPIKey() string {
	if a.APIKeyEnv != "" {
		if key := strings.TrimSpace(os.Getenv(a.APIKeyEnv)); key != "" {
			return key
		}
	}
	return a.APIKey
}

func (a Account) ResolvedHeaders() map[string]string {
	result := make(map[string]string, len(a.Headers)+len(a.HeadersEnv))
	for name, value := range a.Headers {
		result[name] = value
	}
	for name, envName := range a.HeadersEnv {
		result[name] = strings.TrimSpace(os.Getenv(envName))
	}
	return result
}

func SecureEqual(candidate string, allowed []string) bool {
	result := 0
	for _, key := range allowed {
		if len(candidate) == len(key) {
			result |= subtle.ConstantTimeCompare([]byte(candidate), []byte(key))
		}
	}
	return result == 1
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg = Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
