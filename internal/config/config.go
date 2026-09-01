package config

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ConfigVersion             = 1
	MaxAccountIDBytes         = 128
	MaxModelIDBytes           = 256
	MaxConfiguredAccounts     = 4096
	MaxConfiguredRoutes       = 4096
	MaxModelsPerAccount       = 4096
	MaxCapabilitiesPerAccount = 8192
	DefaultMaxBodyBytes       = 64 << 20
	DefaultConfigPath         = "data/config.json"
	DefaultRequestLogMaxBytes = 8 << 20
	DefaultRequestLogBackups  = 2
)

type Config struct {
	Version  int              `json:"version,omitempty"`
	Server   ServerConfig     `json:"server"`
	Accounts []Account        `json:"accounts"`
	Routes   map[string]Route `json:"routes"`
}

type ServerConfig struct {
	Listen                   string   `json:"listen"`
	APIKeys                  []string `json:"api_keys,omitempty"`
	APIKeyEnv                string   `json:"api_key_env,omitempty"`
	AdminToken               string   `json:"admin_token,omitempty"`
	AdminAutoLogin           bool     `json:"admin_auto_login,omitempty"`
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
	RequestLogPath           string   `json:"request_log_path,omitempty"`
	RequestLogMaxBytes       int64    `json:"request_log_max_bytes,omitempty"`
	RequestLogBackups        int      `json:"request_log_backups,omitempty"`
}

type Account struct {
	ID           string              `json:"id"`
	Name         string              `json:"name,omitempty"`
	Type         string              `json:"type"`
	AdapterID    string              `json:"adapter_id,omitempty"`
	InstanceID   string              `json:"instance_id,omitempty"`
	BaseURL      string              `json:"base_url"`
	APIKey       string              `json:"api_key,omitempty"`
	APIKeyEnv    string              `json:"api_key_env,omitempty"`
	AuthHeader   string              `json:"auth_header,omitempty"`
	AuthScheme   string              `json:"auth_scheme,omitempty"`
	Headers      map[string]string   `json:"headers,omitempty"`
	HeadersEnv   map[string]string   `json:"headers_env,omitempty"`
	Models       []string            `json:"models,omitempty"`
	ModelMap     map[string]string   `json:"model_map,omitempty"`
	Capabilities []ChannelCapability `json:"capabilities,omitempty"`
	Operations   []string            `json:"operations,omitempty"`
	Priority     int                 `json:"priority"`
	Weight       int                 `json:"weight"`
	Concurrency  int                 `json:"concurrency"`
	Enabled      bool                `json:"enabled"`
	ProxyURL     string              `json:"proxy_url,omitempty"`
}

type Route struct {
	AllAccounts     bool          `json:"all_accounts,omitempty"`
	Accounts        []string      `json:"accounts,omitempty"`
	UpstreamModel   string        `json:"upstream_model,omitempty"`
	Strategy        string        `json:"strategy,omitempty"`
	Targets         []RouteTarget `json:"targets,omitempty"`
	Model           string        `json:"model,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type ChannelCapability struct {
	Model            string   `json:"model"`
	UpstreamModel    string   `json:"upstream_model"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

type RouteTarget struct {
	Account         string `json:"account"`
	Credential      string `json:"credential,omitempty"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("duration must be a duration string such as 30s")
	}
	v, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func Defaults() Config {
	return Config{
		Version: ConfigVersion,
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
			RequestLogPath:        "request.log",
			RequestLogMaxBytes:    DefaultRequestLogMaxBytes,
			RequestLogBackups:     DefaultRequestLogBackups,
		},
		Routes: make(map[string]Route),
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg = Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := validateStorePaths(path, cfg.Server); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func decodeConfig(data []byte) (Config, error) {
	cfg := Defaults()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		return Config{}, err
	}
	return cfg, nil
}

func Normalize(cfg Config) Config {
	cfg = normalizeDesired(cfg)
	applyEnvironmentOverrides(&cfg)
	return cfg
}

// normalizeDesired applies schema defaults without consulting the process
// environment. Persisted configuration must remain the operator's desired
// state; environment variables are an effective-runtime overlay only.
func normalizeDesired(cfg Config) Config {
	cfg = cloneConfig(cfg)
	applyDefaults(&cfg)
	return cfg
}

func cloneConfig(cfg Config) Config {
	result := cfg
	result.Server.APIKeys = append([]string(nil), cfg.Server.APIKeys...)
	result.Server.AdminAllowedCIDRs = append([]string(nil), cfg.Server.AdminAllowedCIDRs...)
	result.Server.TrustedProxyCIDRs = append([]string(nil), cfg.Server.TrustedProxyCIDRs...)
	result.Accounts = make([]Account, len(cfg.Accounts))
	for index, account := range cfg.Accounts {
		copyAccount := account
		copyAccount.Headers = cloneStringMap(account.Headers)
		copyAccount.HeadersEnv = cloneStringMap(account.HeadersEnv)
		copyAccount.Models = append([]string(nil), account.Models...)
		copyAccount.ModelMap = cloneStringMap(account.ModelMap)
		copyAccount.Operations = append([]string(nil), account.Operations...)
		copyAccount.Capabilities = make([]ChannelCapability, len(account.Capabilities))
		for capabilityIndex, capability := range account.Capabilities {
			copyAccount.Capabilities[capabilityIndex] = capability
			copyAccount.Capabilities[capabilityIndex].ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
		}
		result.Accounts[index] = copyAccount
	}
	result.Routes = make(map[string]Route, len(cfg.Routes))
	for alias, route := range cfg.Routes {
		copyRoute := route
		copyRoute.Accounts = append([]string(nil), route.Accounts...)
		copyRoute.Targets = append([]RouteTarget(nil), route.Targets...)
		result.Routes[alias] = copyRoute
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func applyDefaults(cfg *Config) {
	if cfg.Version == 0 {
		cfg.Version = ConfigVersion
	}
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
	if len(cfg.Server.TrustedProxyCIDRs) == 0 {
		cfg.Server.TrustedProxyCIDRs = append([]string(nil), d.TrustedProxyCIDRs...)
	}
	if cfg.Server.AdminSessionTTL.Duration == 0 {
		cfg.Server.AdminSessionTTL = d.AdminSessionTTL
	}
	if cfg.Server.MaxBodyBytes <= 0 {
		cfg.Server.MaxBodyBytes = d.MaxBodyBytes
	}
	if cfg.Server.MaxInFlightRequests <= 0 {
		cfg.Server.MaxInFlightRequests = d.MaxInFlightRequests
	}
	if cfg.Server.RequestReadTimeout.Duration <= 0 {
		cfg.Server.RequestReadTimeout = d.RequestReadTimeout
	}
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
	if cfg.Server.MaxIdleConnsPerHost <= 0 {
		cfg.Server.MaxIdleConnsPerHost = d.MaxIdleConnsPerHost
	}
	if cfg.Server.MaxConnsPerHost <= 0 {
		cfg.Server.MaxConnsPerHost = d.MaxConnsPerHost
	}
	if cfg.Server.FailureThreshold <= 0 {
		cfg.Server.FailureThreshold = d.FailureThreshold
	}
	if cfg.Server.CircuitCooldown.Duration <= 0 {
		cfg.Server.CircuitCooldown = d.CircuitCooldown
	}
	if cfg.Server.MaxFailoverAttempts <= 0 {
		cfg.Server.MaxFailoverAttempts = d.MaxFailoverAttempts
	}
	if cfg.Server.RequestLogPath == "" {
		cfg.Server.RequestLogPath = d.RequestLogPath
	}
	if cfg.Server.RequestLogMaxBytes <= 0 {
		cfg.Server.RequestLogMaxBytes = d.RequestLogMaxBytes
	}
	if cfg.Server.RequestLogBackups < 0 {
		cfg.Server.RequestLogBackups = d.RequestLogBackups
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
		if len(a.Operations) == 0 {
			a.Operations = DefaultOperations(a.Type)
		} else {
			a.Operations = normalizeStringList(a.Operations)
		}
		if len(a.Capabilities) == 0 && strings.EqualFold(strings.TrimSpace(a.AdapterID), "cli-proxy-api") {
			a.Capabilities = InferCodexCapabilities(a.Models)
		}
		for capabilityIndex := range a.Capabilities {
			capability := &a.Capabilities[capabilityIndex]
			capability.Model = strings.TrimSpace(capability.Model)
			capability.UpstreamModel = strings.TrimSpace(capability.UpstreamModel)
			for effortIndex := range capability.ReasoningEfforts {
				capability.ReasoningEfforts[effortIndex] = strings.ToLower(strings.TrimSpace(capability.ReasoningEfforts[effortIndex]))
			}
			capability.ReasoningEfforts = normalizeStringList(capability.ReasoningEfforts)
		}
	}
	for alias, route := range cfg.Routes {
		route.Model = strings.TrimSpace(route.Model)
		route.ReasoningEffort = strings.ToLower(strings.TrimSpace(route.ReasoningEffort))
		route.Strategy = strings.ToLower(strings.TrimSpace(route.Strategy))
		cfg.Routes[alias] = route
	}
}

func applyEnvironmentOverrides(cfg *Config) {
	if value, ok := boolEnvironmentOverride("LITE2API_ADMIN_AUTO_LOGIN"); ok {
		cfg.Server.AdminAutoLogin = value
	}
	if value := strings.TrimSpace(os.Getenv("LITE2API_ADMIN_ALLOWED_CIDRS")); value != "" {
		cfg.Server.AdminAllowedCIDRs = splitCommaList(value)
	}
	if value := strings.TrimSpace(os.Getenv("LITE2API_TRUSTED_PROXY_CIDRS")); value != "" {
		cfg.Server.TrustedProxyCIDRs = splitCommaList(value)
	}
	applyPositiveInt64Env(&cfg.Server.MaxBodyBytes, "LITE2API_MAX_BODY_BYTES")
	applyPositiveIntEnv(&cfg.Server.MaxInFlightRequests, "LITE2API_MAX_INFLIGHT_REQUESTS")
	applyPositiveIntEnv(&cfg.Server.MaxIdleConns, "LITE2API_MAX_IDLE_CONNS")
	applyPositiveIntEnv(&cfg.Server.MaxIdleConnsPerHost, "LITE2API_MAX_IDLE_CONNS_PER_HOST")
	applyPositiveIntEnv(&cfg.Server.MaxConnsPerHost, "LITE2API_MAX_CONNS_PER_HOST")
	applyPositiveInt64Env(&cfg.Server.RequestLogMaxBytes, "LITE2API_REQUEST_LOG_MAX_BYTES")
	applyNonNegativeIntEnv(&cfg.Server.RequestLogBackups, "LITE2API_REQUEST_LOG_BACKUPS")
}

func boolEnvironmentOverride(name string) (bool, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	return parsed, err == nil
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

func applyNonNegativeIntEnv(target *int, name string) {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && value >= 0 {
		*target = value
	}
}

func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if keys := c.GatewayKeys(); len(keys) > 4096 {
		return errors.New("server gateway API key set cannot contain more than 4096 keys")
	} else {
		for _, key := range keys {
			if len(key) > 64<<10 {
				return errors.New("server gateway API keys cannot exceed 64KiB")
			}
		}
	}
	if len(c.ResolvedAdminToken()) > 64<<10 {
		return errors.New("server admin token cannot exceed 64KiB")
	}
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	if c.Server.AdminSessionTTL.Duration < 5*time.Minute || c.Server.AdminSessionTTL.Duration > 24*time.Hour {
		return errors.New("server.admin_session_ttl must be between 5m and 24h")
	}
	if c.Server.MaxBodyBytes > 256<<20 {
		return errors.New("server.max_body_bytes cannot exceed 256MiB")
	}
	if c.Server.MaxInFlightRequests > 65536 {
		return errors.New("server.max_inflight_requests cannot exceed 65536")
	}
	for name, value := range map[string]time.Duration{
		"request_read_timeout":    c.Server.RequestReadTimeout.Duration,
		"queue_timeout":           c.Server.QueueTimeout.Duration,
		"response_header_timeout": c.Server.ResponseHeaderTimeout.Duration,
		"idle_conn_timeout":       c.Server.IdleConnTimeout.Duration,
	} {
		if value < 0 || value > 30*time.Minute {
			return fmt.Errorf("server.%s must be between 0 and 30m", name)
		}
	}
	if c.Server.StreamIdleTimeout.Duration < 0 || c.Server.StreamIdleTimeout.Duration > 24*time.Hour {
		return errors.New("server.stream_idle_timeout must be between 0 and 24h")
	}
	for name, value := range map[string]int{
		"max_idle_conns":          c.Server.MaxIdleConns,
		"max_idle_conns_per_host": c.Server.MaxIdleConnsPerHost,
		"max_conns_per_host":      c.Server.MaxConnsPerHost,
	} {
		if value > 65536 {
			return fmt.Errorf("server.%s cannot exceed 65536", name)
		}
	}
	if c.Server.FailureThreshold > 100 {
		return errors.New("server.failure_threshold cannot exceed 100")
	}
	if c.Server.CircuitCooldown.Duration > 24*time.Hour {
		return errors.New("server.circuit_cooldown cannot exceed 24h")
	}
	if c.Server.MaxFailoverAttempts > 64 {
		return errors.New("server.max_failover_attempts cannot exceed 64")
	}
	if c.Server.RequestLogMaxBytes < 64<<10 || c.Server.RequestLogMaxBytes > 256<<20 {
		return errors.New("server.request_log_max_bytes must be between 64KiB and 256MiB")
	}
	if c.Server.RequestLogBackups < 0 || c.Server.RequestLogBackups > 8 {
		return errors.New("server.request_log_backups must be between 0 and 8")
	}
	if filepath.IsAbs(c.Server.ClientKeysPath) && filepath.Clean(c.Server.ClientKeysPath) == string(filepath.Separator) {
		return errors.New("server.client_keys_path cannot be the filesystem root")
	}
	if filepath.IsAbs(c.Server.RequestLogPath) && filepath.Clean(c.Server.RequestLogPath) == string(filepath.Separator) {
		return errors.New("server.request_log_path cannot be the filesystem root")
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
	if len(c.Accounts) > MaxConfiguredAccounts {
		return fmt.Errorf("accounts cannot contain more than %d entries", MaxConfiguredAccounts)
	}
	if len(c.Routes) > MaxConfiguredRoutes {
		return fmt.Errorf("routes cannot contain more than %d entries", MaxConfiguredRoutes)
	}
	seen := make(map[string]struct{}, len(c.Accounts))
	for _, a := range c.Accounts {
		if a.ID == "" {
			return errors.New("account id is required")
		}
		if strings.TrimSpace(a.ID) != a.ID || len(a.ID) > MaxAccountIDBytes {
			return fmt.Errorf("account id %q must be trimmed and at most %d bytes", a.ID, MaxAccountIDBytes)
		}
		if len(a.Name) > 256 || len(a.AdapterID) > MaxAccountIDBytes || len(a.InstanceID) > MaxAccountIDBytes {
			return fmt.Errorf("account %q has oversized name or adapter identity", a.ID)
		}
		if len(a.BaseURL) > 4096 || len(a.ProxyURL) > 4096 || len(a.APIKey) > 64<<10 || len(a.APIKeyEnv) > 256 {
			return fmt.Errorf("account %q has an oversized URL or credential field", a.ID)
		}
		if len(a.ResolvedAPIKey()) > 64<<10 {
			return fmt.Errorf("account %q resolved API key cannot exceed 64KiB", a.ID)
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
		for _, operation := range a.Operations {
			if !ValidOperation(operation) {
				return fmt.Errorf("account %q has unsupported operation %q", a.ID, operation)
			}
		}
		if len(a.Operations) > 16 {
			return fmt.Errorf("account %q has too many operations", a.ID)
		}
		if len(a.AuthHeader) > 256 || (a.AuthHeader != "" && !validHeaderName(a.AuthHeader) && a.AuthHeader != "none") {
			return fmt.Errorf("account %q has invalid auth_header", a.ID)
		}
		if len(a.Headers) > 64 || len(a.HeadersEnv) > 64 {
			return fmt.Errorf("account %q cannot define more than 64 custom headers", a.ID)
		}
		for name, value := range a.Headers {
			if !validHeaderName(name) {
				return fmt.Errorf("account %q has invalid header name %q", a.ID, name)
			}
			if len(name) > 256 || len(value) > 16<<10 {
				return fmt.Errorf("account %q has an oversized header %q", a.ID, name)
			}
		}
		for name, envName := range a.HeadersEnv {
			if !validHeaderName(name) {
				return fmt.Errorf("account %q has invalid environment header name %q", a.ID, name)
			}
			if len(name) > 256 || len(envName) > 256 {
				return fmt.Errorf("account %q has an oversized environment header %q", a.ID, name)
			}
			if len(strings.TrimSpace(os.Getenv(envName))) > 16<<10 {
				return fmt.Errorf("account %q resolved header %q cannot exceed 16KiB", a.ID, name)
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
		if len(a.Models) > MaxModelsPerAccount || len(a.ModelMap) > MaxModelsPerAccount {
			return fmt.Errorf("account %q cannot advertise more than %d models", a.ID, MaxModelsPerAccount)
		}
		for index, model := range a.Models {
			if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model || len(model) > MaxModelIDBytes {
				return fmt.Errorf("account %q model %d must be non-empty and at most %d bytes", a.ID, index+1, MaxModelIDBytes)
			}
		}
		for logical, upstream := range a.ModelMap {
			if strings.TrimSpace(logical) == "" || strings.TrimSpace(upstream) == "" || strings.TrimSpace(logical) != logical || strings.TrimSpace(upstream) != upstream || len(logical) > MaxModelIDBytes || len(upstream) > MaxModelIDBytes {
				return fmt.Errorf("account %q has an invalid or oversized model mapping", a.ID)
			}
		}
		if len(a.Capabilities) > MaxCapabilitiesPerAccount {
			return fmt.Errorf("account %q cannot define more than %d capabilities", a.ID, MaxCapabilitiesPerAccount)
		}
		for index, capability := range a.Capabilities {
			if strings.TrimSpace(capability.Model) == "" || strings.TrimSpace(capability.UpstreamModel) == "" {
				return fmt.Errorf("account %q capability %d requires model and upstream_model", a.ID, index+1)
			}
			if len(capability.Model) > MaxModelIDBytes || len(capability.UpstreamModel) > MaxModelIDBytes {
				return fmt.Errorf("account %q capability %d model identifiers cannot exceed %d bytes", a.ID, index+1, MaxModelIDBytes)
			}
			if !AccountSupportsTargetModel(a, capability.UpstreamModel) {
				return fmt.Errorf("account %q capability %d references unadvertised upstream model %q", a.ID, index+1, capability.UpstreamModel)
			}
			if len(capability.ReasoningEfforts) == 0 {
				return fmt.Errorf("account %q capability %d requires at least one reasoning effort", a.ID, index+1)
			}
			if len(capability.ReasoningEfforts) > 16 {
				return fmt.Errorf("account %q capability %d has too many reasoning efforts", a.ID, index+1)
			}
			for _, effort := range capability.ReasoningEfforts {
				if !ValidReasoningEffort(effort) {
					return fmt.Errorf("account %q capability %d has unsupported reasoning effort %q", a.ID, index+1, effort)
				}
			}
		}
	}
	for model, route := range c.Routes {
		if strings.TrimSpace(model) == "" {
			return errors.New("route model cannot be empty")
		}
		if strings.TrimSpace(model) != model || len(model) > MaxModelIDBytes {
			return fmt.Errorf("route model %q must be trimmed and at most %d bytes", model, MaxModelIDBytes)
		}
		if len(route.Model) > MaxModelIDBytes || len(route.UpstreamModel) > MaxModelIDBytes ||
			strings.TrimSpace(route.Model) != route.Model || strings.TrimSpace(route.UpstreamModel) != route.UpstreamModel {
			return fmt.Errorf("route %q contains a model identifier longer than %d bytes", model, MaxModelIDBytes)
		}
		if route.Strategy != "" && route.Strategy != "least_loaded" && route.Strategy != "round_robin" && route.Strategy != "priority" && route.Strategy != "sticky" {
			return fmt.Errorf("route %q has unsupported strategy %q", model, route.Strategy)
		}
		if route.AllAccounts && (len(route.Accounts) > 0 || len(route.Targets) > 0) {
			return fmt.Errorf("route %q cannot combine all_accounts with accounts or targets", model)
		}
		if len(route.Accounts) > 0 && len(route.Targets) > 0 {
			return fmt.Errorf("route %q cannot combine legacy accounts with targets", model)
		}
		if len(route.Targets) > 0 && (route.UpstreamModel != "" || route.AllAccounts) {
			return fmt.Errorf("route %q cannot combine targets with legacy upstream_model or all_accounts", model)
		}
		if len(route.Targets) == 0 && (route.Model != "" || route.ReasoningEffort != "") {
			return fmt.Errorf("route %q requires targets when model or reasoning_effort is set", model)
		}
		if route.Model == "" && route.ReasoningEffort != "" {
			return fmt.Errorf("route %q cannot set reasoning_effort without a logical model", model)
		}
		if !route.AllAccounts && len(route.Accounts) == 0 && len(route.Targets) == 0 {
			return fmt.Errorf("route %q has no targets; set all_accounts explicitly for wildcard routing", model)
		}
		for _, id := range route.Accounts {
			if len(id) > MaxAccountIDBytes || strings.TrimSpace(id) != id {
				return fmt.Errorf("route %q contains an oversized account id", model)
			}
			if _, ok := seen[id]; !ok {
				return fmt.Errorf("route %q references unknown account %q", model, id)
			}
		}
		if route.UpstreamModel != "" {
			accountIDs := route.Accounts
			if route.AllAccounts {
				accountIDs = accountIDs[:0]
				for _, account := range c.Accounts {
					if account.Enabled {
						accountIDs = append(accountIDs, account.ID)
					}
				}
			}
			for _, accountID := range accountIDs {
				account, _ := accountByID(c.Accounts, accountID)
				if !AccountSupportsTargetModel(account, route.UpstreamModel) {
					return fmt.Errorf("route %q: account %q does not advertise upstream_model %q", model, accountID, route.UpstreamModel)
				}
			}
		}
		if len(route.Accounts) > 64 || len(route.Targets) > 64 {
			return fmt.Errorf("route %q has too many targets (maximum 64)", model)
		}
		if route.Model != "" && !ValidReasoningEffort(route.ReasoningEffort) {
			return fmt.Errorf("route %q has unsupported reasoning_effort %q", model, route.ReasoningEffort)
		}
		for index, target := range route.Targets {
			accountID := strings.TrimSpace(target.Account)
			if accountID == "" {
				return fmt.Errorf("route %q target %d requires an account", model, index+1)
			}
			credential := strings.TrimSpace(target.Credential)
			if accountID != target.Account || credential != target.Credential || strings.TrimSpace(target.Model) != target.Model || len(accountID) > MaxAccountIDBytes || len(credential) > MaxAccountIDBytes || len(target.Model) > MaxModelIDBytes {
				return fmt.Errorf("route %q target %d contains an untrimmed or oversized account, credential, or model", model, index+1)
			}
			account, ok := accountByID(c.Accounts, accountID)
			if !ok {
				return fmt.Errorf("route %q target %d references unknown account %q", model, index+1, accountID)
			}
			if credential != "" && !strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") {
				return fmt.Errorf("route %q target %d: credential pinning requires a cli-proxy-api account", model, index+1)
			}
			if route.Model != "" {
				if _, _, ok := ResolveRouteTarget(account, route, target); !ok {
					return fmt.Errorf("route %q target %d: channel %q does not support %q at reasoning %q", model, index+1, accountID, route.Model, route.ReasoningEffort)
				}
				continue
			}
			upstreamModel := strings.TrimSpace(target.Model)
			if upstreamModel == "" {
				return fmt.Errorf("route %q target %d requires a model", model, index+1)
			}
			if !AccountSupportsTargetModel(account, upstreamModel) {
				return fmt.Errorf("route %q target %d: account %q does not advertise model %q", model, index+1, accountID, upstreamModel)
			}
			if !ValidReasoningEffort(target.ReasoningEffort) {
				return fmt.Errorf("route %q target %d has unsupported reasoning_effort %q", model, index+1, target.ReasoningEffort)
			}
		}
	}
	return nil
}

func accountByID(accounts []Account, id string) (Account, bool) {
	for _, account := range accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func AccountSupportsTargetModel(account Account, model string) bool {
	if len(account.Models) == 0 {
		return true
	}
	for _, candidate := range account.Models {
		if candidate == "*" || candidate == model {
			return true
		}
	}
	for _, mapped := range account.ModelMap {
		if mapped == model {
			return true
		}
	}
	return false
}

func ValidReasoningEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "auto", "none", "minimal", "low", "medium", "high", "max", "xhigh", "ultra":
		return true
	default:
		return false
	}
}

func ResolveRouteTarget(account Account, route Route, target RouteTarget) (model, reasoningEffort string, ok bool) {
	if strings.TrimSpace(route.Model) == "" {
		model = strings.TrimSpace(target.Model)
		return model, target.ReasoningEffort, model != ""
	}
	effort := strings.ToLower(strings.TrimSpace(route.ReasoningEffort))
	if effort == "" {
		effort = "auto"
	}
	logicalModel := strings.TrimSpace(route.Model)
	for _, capability := range account.Capabilities {
		if capability.Model != logicalModel || !slices.Contains(capability.ReasoningEfforts, effort) {
			continue
		}
		return capability.UpstreamModel, effort, true
	}
	return "", effort, false
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
	return s.saveLocked(cfg)
}

// SaveEffective persists a runtime-effective configuration without writing
// process environment overrides back into the desired JSON document. It is
// intended for control-plane mutations based on Config returned by Load.
func (s *Store) SaveEffective(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	desired := Defaults()
	data, err := os.ReadFile(s.path)
	if err == nil {
		desired, err = decodeConfig(data)
		if err != nil {
			return fmt.Errorf("parse current desired config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	preserveEnvironmentOwnedFields(&cfg.Server, desired.Server)
	return s.saveLocked(cfg)
}

func (s *Store) saveLocked(cfg Config) error {
	cfg = normalizeDesired(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := validateStorePaths(s.path, cfg.Server); err != nil {
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
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open config directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return fmt.Errorf("sync config directory: %w", err)
	}
	return directory.Close()
}

func preserveEnvironmentOwnedFields(candidate *ServerConfig, desired ServerConfig) {
	if _, ok := boolEnvironmentOverride("LITE2API_ADMIN_AUTO_LOGIN"); ok {
		candidate.AdminAutoLogin = desired.AdminAutoLogin
	}
	if strings.TrimSpace(os.Getenv("LITE2API_ADMIN_ALLOWED_CIDRS")) != "" {
		candidate.AdminAllowedCIDRs = append([]string(nil), desired.AdminAllowedCIDRs...)
	}
	if strings.TrimSpace(os.Getenv("LITE2API_TRUSTED_PROXY_CIDRS")) != "" {
		candidate.TrustedProxyCIDRs = append([]string(nil), desired.TrustedProxyCIDRs...)
	}
	if positiveInt64EnvironmentOverride("LITE2API_MAX_BODY_BYTES") {
		candidate.MaxBodyBytes = desired.MaxBodyBytes
	}
	if positiveIntEnvironmentOverride("LITE2API_MAX_INFLIGHT_REQUESTS") {
		candidate.MaxInFlightRequests = desired.MaxInFlightRequests
	}
	if positiveIntEnvironmentOverride("LITE2API_MAX_IDLE_CONNS") {
		candidate.MaxIdleConns = desired.MaxIdleConns
	}
	if positiveIntEnvironmentOverride("LITE2API_MAX_IDLE_CONNS_PER_HOST") {
		candidate.MaxIdleConnsPerHost = desired.MaxIdleConnsPerHost
	}
	if positiveIntEnvironmentOverride("LITE2API_MAX_CONNS_PER_HOST") {
		candidate.MaxConnsPerHost = desired.MaxConnsPerHost
	}
	if positiveInt64EnvironmentOverride("LITE2API_REQUEST_LOG_MAX_BYTES") {
		candidate.RequestLogMaxBytes = desired.RequestLogMaxBytes
	}
	if nonNegativeIntEnvironmentOverride("LITE2API_REQUEST_LOG_BACKUPS") {
		candidate.RequestLogBackups = desired.RequestLogBackups
	}
}

func positiveIntEnvironmentOverride(name string) bool {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value > 0
}

func positiveInt64EnvironmentOverride(name string) bool {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	return err == nil && value > 0
}

func nonNegativeIntEnvironmentOverride(name string) bool {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value >= 0
}

func validateStorePaths(configPath string, server ServerConfig) error {
	paths := map[string]string{
		"config":           configPath,
		"client_keys_path": resolveStoredPath(configPath, server.ClientKeysPath),
		"request_log_path": resolveStoredPath(configPath, server.RequestLogPath),
	}
	canonical := make(map[string]string, len(paths))
	for name, path := range paths {
		resolved, err := canonicalPath(path)
		if err != nil {
			return fmt.Errorf("server.%s: %w", name, err)
		}
		canonical[name] = resolved
	}
	for _, pair := range [][2]string{{"config", "client_keys_path"}, {"config", "request_log_path"}, {"client_keys_path", "request_log_path"}} {
		if canonical[pair[0]] == canonical[pair[1]] {
			return fmt.Errorf("server.%s and %s must refer to different files", pair[0], pair[1])
		}
	}
	return nil
}

func resolveStoredPath(configPath, configured string) string {
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(filepath.Dir(configPath), configured)
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	parent, name := filepath.Dir(absolute), filepath.Base(absolute)
	if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolvedParent, name), nil
	}
	return absolute, nil
}
