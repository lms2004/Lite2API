package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type runtimeState struct {
	cfg                 config.Config
	scheduler           *Scheduler
	clients             map[string]*http.Client
	models              []string
	legacyKeyHashes     map[[sha256.Size]byte]struct{}
	adminToken          string
	adminAuthGeneration uint64
	adminAllowed        []*net.IPNet
	trustedProxies      []*net.IPNet
	routeFingerprints   map[string]string
}

type Gateway struct {
	configPath   string
	store        *config.Store
	state        atomic.Pointer[runtimeState]
	requestLog   atomic.Pointer[requestLogWriter]
	reloadMu     sync.Mutex
	stats        *Stats
	globalActive atomic.Int64
	bodyBytes    atomic.Int64
	clientKeys   *ClientKeyStore
	adminAuth    *AdminAuthenticator
	adapterProbe *adapterProbeCache
	closeOnce    sync.Once
	closed       atomic.Bool
}

const (
	maxGatewayModelBytes       = 256
	defaultBufferedBodyBudget  = 64 << 20
	maxRequestInspectionBytes  = 1 << 20
	maxRequestEnvelopeFields   = 256
	maxRequestEnvelopeKeyBytes = 256
	maxInternalRewriteGrowth   = 512 << 10
	maxSessionKeyBytes         = 4 << 10
)

var (
	errTooManyRequestFields = errors.New("request has too many top-level fields")
	errRequestFieldTooLong  = errors.New("request field name is too long")
	errInvalidEnvelopeShape = errors.New("request body must be a JSON object")
	errSessionKeyTooLong    = errors.New("session key must not exceed 4096 bytes")
)

func New(configPath string) (*Gateway, error) {
	g := &Gateway{
		configPath: configPath, store: config.NewStore(configPath), stats: NewStats(512),
		adapterProbe: newAdapterProbeCache(time.Minute),
	}
	// Recover retained observations before opening the writer. Opening a full
	// current log rotates it, and backups=0 would otherwise destroy the only
	// readiness evidence before recovery had a chance to scan it.
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	state, err := g.buildState(cfg)
	if err != nil {
		return nil, err
	}
	records, routeLatest, recoveryErr := loadRequestState(
		resolveRequestLogPath(configPath, state.cfg.Server.RequestLogPath),
		state.cfg.Server.RequestLogBackups, maxRecoveredRequestRecords, state.routeFingerprints,
	)
	g.reloadMu.Lock()
	err = g.commitStateLocked(state, nil)
	g.reloadMu.Unlock()
	if err != nil {
		return nil, err
	}
	if recoveryErr != nil {
		slog.Warn("latest request baseline unavailable", "error", recoveryErr)
	} else {
		// Restore request observations for health/readiness and historical trend
		// rendering. Cumulative counters intentionally start at this process
		// lifetime and are not reconstructed from the log.
		for _, record := range records {
			g.stats.Record(record)
		}
		for _, record := range routeLatest {
			g.stats.RecordRouteObservation(record)
		}
	}
	keyPath := ResolveClientKeysPath(configPath, state.cfg.Server.ClientKeysPath)
	clientKeys, err := NewClientKeyStore(keyPath)
	if err != nil {
		g.Close()
		return nil, err
	}
	g.clientKeys = clientKeys
	g.adminAuth = NewAdminAuthenticator(state.cfg.Server.AdminSessionTTL.Duration)
	return g, nil
}

func (g *Gateway) Reload() error {
	g.reloadMu.Lock()
	defer g.reloadMu.Unlock()
	if g.closed.Load() {
		return errors.New("gateway is closed")
	}
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	state, err := g.buildState(cfg)
	if err != nil {
		return err
	}
	return g.commitStateLocked(state, nil)
}

// commitStateLocked is the single lifecycle commit boundary used by both
// SIGHUP reloads and control-plane read/modify/write transactions. The caller
// must hold reloadMu. All fallible runtime resources are staged before the
// state pointer is published, and a failed persistence step restores the
// previous request-log configuration.
func (g *Gateway) commitStateLocked(state *runtimeState, persist func() error) error {
	if state == nil {
		return errors.New("runtime state is nil")
	}
	if g.closed.Load() {
		for _, client := range state.clients {
			client.CloseIdleConnections()
		}
		return errors.New("gateway is closed")
	}
	committed := false
	defer func() {
		if !committed {
			for _, client := range state.clients {
				client.CloseIdleConnections()
			}
		}
	}()
	oldState := g.state.Load()
	if g.clientKeys != nil {
		if oldState != nil && (state.cfg.Server.Listen != oldState.cfg.Server.Listen || state.cfg.Server.RequestReadTimeout.Duration != oldState.cfg.Server.RequestReadTimeout.Duration) {
			return errors.New("server.listen and server.request_read_timeout require a process restart")
		}
		keyPath := ResolveClientKeysPath(g.configPath, state.cfg.Server.ClientKeysPath)
		if filepath.Clean(keyPath) != filepath.Clean(g.clientKeys.Path()) {
			return errors.New("server.client_keys_path cannot change during hot reload")
		}
	}
	logPath := resolveRequestLogPath(g.configPath, state.cfg.Server.RequestLogPath)
	oldLog := g.requestLog.Load()
	requestLog := oldLog
	createdLog := false
	reconfiguredLog := false
	oldLogPath, oldLogMaxSize, oldLogBackups := "", int64(0), 0
	if oldLog == nil {
		var createErr error
		requestLog, createErr = newRequestLogWriter(logPath, state.cfg.Server.RequestLogMaxBytes, state.cfg.Server.RequestLogBackups)
		if createErr != nil {
			return fmt.Errorf("request log: %w", createErr)
		}
		createdLog = true
	} else if !oldLog.matches(logPath, state.cfg.Server.RequestLogMaxBytes, state.cfg.Server.RequestLogBackups) {
		// Reconfiguration is serialized inside the long-lived log actor. The
		// queue remains owned by one writer, so no record can be split between
		// two descriptors or silently dropped during a same-path rotation.
		oldLogPath, oldLogMaxSize, oldLogBackups = oldLog.configuration()
		if err := oldLog.Reconfigure(logPath, state.cfg.Server.RequestLogMaxBytes, state.cfg.Server.RequestLogBackups); err != nil {
			return fmt.Errorf("request log: %w", err)
		}
		reconfiguredLog = true
	}
	rollbackLog := func() error {
		if createdLog {
			requestLog.Close()
			return nil
		}
		if reconfiguredLog {
			return oldLog.Reconfigure(oldLogPath, oldLogMaxSize, oldLogBackups)
		}
		return nil
	}
	if persist != nil {
		if err := persist(); err != nil {
			if rollbackErr := rollbackLog(); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restore request log after failed commit: %w", rollbackErr))
			}
			return err
		}
	}
	// Revoke before publishing a changed authentication boundary. This creates
	// at most a fail-closed interval during commit; publishing first would allow
	// an old cookie to authenticate against the new token/CIDR generation.
	if g.adminAuth != nil {
		state.adminAuthGeneration = g.adminAuth.Reconfigure(state.cfg.Server.AdminSessionTTL.Duration, adminSecurityBoundaryChanged(oldState, state))
	} else {
		// New() builds the first runtime before constructing the authenticator;
		// both start at generation one.
		state.adminAuthGeneration = 1
	}
	g.stats.ConfigureRouteFingerprints(state.routeFingerprints)
	g.swapState(state)
	committed = true
	if requestLog != oldLog {
		g.requestLog.Store(requestLog)
	}
	return nil
}

func adminSecurityBoundaryChanged(oldState, newState *runtimeState) bool {
	if oldState == nil || newState == nil {
		return false
	}
	return oldState.adminToken != newState.adminToken ||
		oldState.cfg.Server.AdminAutoLogin != newState.cfg.Server.AdminAutoLogin ||
		oldState.cfg.Server.AdminSessionTTL.Duration != newState.cfg.Server.AdminSessionTTL.Duration ||
		!slices.Equal(oldState.cfg.Server.AdminAllowedCIDRs, newState.cfg.Server.AdminAllowedCIDRs) ||
		!slices.Equal(oldState.cfg.Server.TrustedProxyCIDRs, newState.cfg.Server.TrustedProxyCIDRs)
}

// Close drains durable observations and closes idle upstream connections. It
// is safe to call more than once and is the lifecycle counterpart to New.
func (g *Gateway) Close() {
	if g == nil {
		return
	}
	g.closeOnce.Do(func() {
		g.reloadMu.Lock()
		defer g.reloadMu.Unlock()
		g.closed.Store(true)
		if logger := g.requestLog.Swap(nil); logger != nil {
			logger.Close()
		}
		if state := g.state.Load(); state != nil {
			for _, client := range state.clients {
				client.CloseIdleConnections()
			}
		}
	})
}

func (g *Gateway) buildState(cfg config.Config) (*runtimeState, error) {
	cfg = config.Normalize(cfg)
	var previous *Scheduler
	if old := g.state.Load(); old != nil {
		previous = old.scheduler
	}
	adminAllowed, err := parseNetworks(cfg.Server.AdminAllowedCIDRs)
	if err != nil {
		return nil, fmt.Errorf("admin allowed networks: %w", err)
	}
	trustedProxies, err := parseNetworks(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("trusted proxy networks: %w", err)
	}
	legacyHashes := make(map[[sha256.Size]byte]struct{})
	for _, key := range cfg.GatewayKeys() {
		legacyHashes[sha256.Sum256([]byte(key))] = struct{}{}
	}
	state := &runtimeState{
		cfg: cfg, scheduler: NewSchedulerWithPrevious(cfg, previous),
		clients:         make(map[string]*http.Client, len(cfg.Accounts)),
		legacyKeyHashes: legacyHashes, adminToken: cfg.ResolvedAdminToken(),
		adminAllowed: adminAllowed, trustedProxies: trustedProxies,
	}
	state.routeFingerprints = buildRouteFingerprints(cfg)
	built := false
	defer func() {
		if !built {
			for _, client := range state.clients {
				client.CloseIdleConnections()
			}
		}
	}()
	for _, account := range cfg.Accounts {
		runtimeAccount := state.scheduler.Get(account.ID)
		if account.Enabled && account.AuthHeader != "none" && runtimeAccount.UpstreamKey == "" {
			return nil, fmt.Errorf("account %q is enabled but has no API key", account.ID)
		}
		client, err := newHTTPClient(cfg.Server, account)
		if err != nil {
			return nil, fmt.Errorf("account %q transport: %w", account.ID, err)
		}
		state.clients[account.ID] = client
	}
	state.models = state.scheduler.Models()
	built = true
	return state, nil
}

func (g *Gateway) swapState(state *runtimeState) {
	old := g.state.Swap(state)
	if old != nil {
		for _, client := range old.clients {
			client.CloseIdleConnections()
		}
	}
}

func newHTTPClient(server config.ServerConfig, account config.Account) (*http.Client, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          server.MaxIdleConns,
		MaxIdleConnsPerHost:   server.MaxIdleConnsPerHost,
		MaxConnsPerHost:       server.MaxConnsPerHost,
		IdleConnTimeout:       server.IdleConnTimeout.Duration,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: server.ResponseHeaderTimeout.Duration,
		ExpectContinueTimeout: time.Second,
	}
	if account.ProxyURL != "" {
		proxy, err := url.Parse(account.ProxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("upstream redirects are disabled") }}, nil
}

func (g *Gateway) Config() config.Config       { return cloneConfig(g.state.Load().cfg) }
func (g *Gateway) Stats() StatsSnapshot        { return g.stats.Snapshot() }
func (g *Gateway) Accounts() []AccountSnapshot { return g.state.Load().scheduler.Snapshot() }
func (g *Gateway) RequestLog() RequestLogStatus {
	if logger := g.requestLog.Load(); logger != nil {
		return logger.Status()
	}
	return RequestLogStatus{}
}

func (g *Gateway) ServeGateway(w http.ResponseWriter, r *http.Request) {
	state := g.state.Load()
	protocolOperation, _ := operationForGatewayPath(r.URL.Path)
	writeError := func(status int, message, kind, code string) {
		writeProtocolError(w, protocolOperation, status, message, kind, code)
	}
	lease, authFailure := g.clientKeys.Authenticate(apiBearerToken(r), state.legacyKeyHashes)
	switch authFailure {
	case KeyAuthInvalid:
		writeError(http.StatusUnauthorized, "invalid API key", "authentication_error", "invalid_api_key")
		return
	case KeyAuthRateLimited:
		w.Header().Set("Retry-After", "60")
		writeError(http.StatusTooManyRequests, "API key rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		return
	case KeyAuthConcurrency:
		w.Header().Set("Retry-After", "1")
		writeError(http.StatusTooManyRequests, "API key concurrency limit reached", "rate_limit_error", "concurrency_limit_exceeded")
		return
	}
	ok := false
	defer func() { lease.Complete(ok) }()
	if strings.TrimSuffix(r.URL.Path, "/") == "/v1/models" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "invalid_request_error")
			return
		}
		g.serveModels(w, state, lease)
		ok = true
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "invalid_request_error")
		return
	}
	operation, supported := operationForGatewayPath(r.URL.Path)
	if !supported {
		writeError(http.StatusNotFound, "unsupported endpoint", "invalid_request_error", "invalid_request_error")
		return
	}
	if !g.tryAcquireGlobal(int64(state.cfg.Server.MaxInFlightRequests)) {
		w.Header().Set("Retry-After", "1")
		writeError(http.StatusTooManyRequests, "gateway concurrency limit reached", "rate_limit_error", "rate_limit_error")
		return
	}
	defer g.globalActive.Add(-1)
	if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
		writeProtocolError(w, operation, http.StatusBadRequest, "invalid request query", "invalid_request_error", "invalid_request_query")
		return
	}
	if r.ContentLength > state.cfg.Server.MaxBodyBytes {
		writeProtocolError(w, operation, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
		return
	}
	bodyBudget := requestBodyMemoryBudget(state.cfg.Server.MaxBodyBytes)
	bodyLease := &bodyByteLease{gateway: g, limit: bodyBudget}
	defer bodyLease.Release()
	knownBodyLength := r.ContentLength >= 0 && len(r.TransferEncoding) == 0
	if knownBodyLength && !bodyLease.Acquire(r.ContentLength) {
		w.Header().Set("Retry-After", "1")
		writeError(http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
		return
	}
	if !knownBodyLength && g.bodyBytes.Load() >= bodyBudget {
		w.Header().Set("Retry-After", "1")
		writeError(http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
		return
	}

	body, err := readBody(w, r, state.cfg.Server.MaxBodyBytes, operation, bodyLease)
	if err != nil {
		return
	}
	requestBytes := int64(len(body))
	// Enforce map cardinality before json.Unmarshal constructs client-sized
	// strings and map buckets. This scanner retains no keys or values and stops
	// after the first over-budget top-level field.
	if err := validateTopLevelEnvelope(body); err != nil {
		switch {
		case errors.Is(err, errTooManyRequestFields):
			writeError(http.StatusBadRequest, err.Error(), "invalid_request_error", "too_many_request_fields")
		case errors.Is(err, errRequestFieldTooLong):
			writeError(http.StatusBadRequest, err.Error(), "invalid_request_error", "request_field_too_long")
		default:
			writeError(http.StatusBadRequest, "request body must be valid JSON", "invalid_request_error", "invalid_request_error")
		}
		return
	}
	// json.RawMessage copies every value out of the input buffer. Reserve that
	// second retained representation before decoding so queued large requests
	// cannot hide parse amplification behind a zero byte-budget counter.
	if !bodyLease.Acquire(requestBytes) {
		w.Header().Set("Retry-After", "1")
		writeProtocolError(w, operation, http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
		return
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeError(http.StatusBadRequest, "request body must be valid JSON", "invalid_request_error", "invalid_request_error")
		return
	}
	if len(envelope) > maxRequestEnvelopeFields {
		writeError(http.StatusBadRequest, "request has too many top-level fields", "invalid_request_error", "too_many_request_fields")
		return
	}
	for key := range envelope {
		if len(key) > maxRequestEnvelopeKeyBytes {
			writeError(http.StatusBadRequest, "request field name is too long", "invalid_request_error", "request_field_too_long")
			return
		}
	}
	var model string
	modelJSON := envelope["model"]
	if len(modelJSON) > maxGatewayModelBytes*6+2 {
		writeError(http.StatusBadRequest, "model must not exceed 256 bytes", "invalid_request_error", "model_too_long")
		return
	}
	if err := json.Unmarshal(modelJSON, &model); err != nil || strings.TrimSpace(model) == "" {
		writeError(http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request_error")
		return
	}
	if len(model) > maxGatewayModelBytes {
		writeError(http.StatusBadRequest, "model must not exceed 256 bytes", "invalid_request_error", "model_too_long")
		return
	}
	if !lease.AllowsModel(model) {
		writeError(http.StatusForbidden, "model is not allowed for this API key", "permission_error", "model_not_allowed")
		return
	}
	if (operation == config.OperationOpenAIChat || operation == config.OperationOpenAIResponses) && applyExecutionProfile(envelope, state.cfg.Routes) {
		// Marshal temporarily retains the old body, RawMessages, and the new
		// encoded body. Reserve the full legal output before allocation.
		profileReservation := rewriteBodyReservation(state.cfg.Server.MaxBodyBytes)
		if !bodyLease.Acquire(profileReservation) {
			w.Header().Set("Retry-After", "1")
			writeProtocolError(w, operation, http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
			return
		}
		body, err = json.Marshal(envelope)
		if err != nil {
			writeError(http.StatusBadRequest, "request body could not be normalized", "invalid_request_error", "invalid_request_error")
			return
		}
		if int64(len(body)) > profileReservation {
			writeProtocolError(w, operation, http.StatusRequestEntityTooLarge, "normalized request body too large", "invalid_request_error", "request_too_large")
			return
		}
		retainedBytes := requestBytes + int64(len(body))
		bodyLease.ShrinkTo(retainedBytes)
	}
	session, err := sessionKey(r, envelope)
	if err != nil {
		writeError(http.StatusBadRequest, err.Error(), "invalid_request_error", "session_key_too_long")
		return
	}
	requestID := requestID()
	start := time.Now()
	g.stats.Begin()
	input := contentSummary{}
	if len(body) <= maxRequestInspectionBytes {
		input = inspectRequest(envelope, operation)
	} else {
		// Modality inspection is observability-only. Do not materialize a
		// second object tree for multi-megabyte prompts or embedded media.
		input.Text = 1
	}
	var stream bool
	_ = json.Unmarshal(envelope["stream"], &stream)
	record := RequestRecord{
		Operation:        operation,
		InputType:        input.Kind(),
		InputParts:       input.Total(),
		TextParts:        input.Text,
		ImageParts:       input.Image,
		AudioParts:       input.Audio,
		VideoParts:       input.Video,
		FileParts:        input.File,
		RequestBytes:     requestBytes,
		Stream:           stream,
		RouteFingerprint: state.routeFingerprints[model],
	}
	defer func() {
		if record.Outcome == "" {
			record.Outcome = requestOutcome(ok, record, r.Context().Err())
		}
		g.stats.End(ok)
		record.Time = time.Now().UTC().Format(time.RFC3339Nano)
		record.RequestID = requestID
		record.Model = model
		record.ClientKeyID = lease.ID
		record.ClientKeyName = lease.Name
		record.Path = r.URL.Path
		record.LatencyMS = time.Since(start).Milliseconds()
		record.Error = truncate(record.Error, 1024)
		g.stats.Record(record)
		if _, routed := state.cfg.Routes[model]; routed && routeHealthObservation(record) {
			g.stats.RecordRouteObservation(record)
		}
		if logger := g.requestLog.Load(); logger != nil {
			logger.Enqueue(record)
		}
	}()

	excluded := make(map[string]struct{})
	baseBodyReservation := bodyLease.reserved
	releaseRetainedBody := func() {
		body = nil
		envelope = nil
		bodyLease.Release()
	}
	var last *bufferedResponse
	var lastAccountID, lastCredentialID, lastUpstreamModel, lastReasoningEffort string
	maxAttempts := min(state.cfg.Server.MaxFailoverAttempts, len(state.cfg.Accounts))
	maxAttempts = state.scheduler.AttemptLimit(model, maxAttempts)
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		wait := state.cfg.Server.QueueTimeout.Duration
		if last != nil {
			// Once an upstream has returned an error response, an optional
			// failover may use currently-free capacity, but it must never keep
			// the client queued for another account to recover.
			wait = 0
		}
		selection, err := state.scheduler.Select(r.Context(), model, operation, session, excluded, wait)
		if err != nil {
			if last != nil {
				releaseRetainedBody()
				_ = last.write(w, state.cfg.Server.StreamIdleTimeout.Duration)
				record.AccountID = lastAccountID
				record.CredentialID = lastCredentialID
				record.UpstreamModel = lastUpstreamModel
				record.ReasoningEffort = lastReasoningEffort
				record.Status = last.status
				record.Error = "upstream returned " + strconv.Itoa(last.status)
				applyBufferedResponseMetadata(&record, last.header, last.body)
				if record.OutputType == "" && operation == config.OperationImages {
					record.OutputType = "image"
				}
				return
			}
			record.Error = err.Error()
			releaseRetainedBody()
			writeError(http.StatusServiceUnavailable, err.Error(), "upstream_unavailable", "upstream_unavailable")
			record.Status = http.StatusServiceUnavailable
			return
		}
		excluded[selection.Key] = struct{}{}
		record.AccountID = selection.Account.Config.ID
		record.CredentialID = selection.Credential
		record.UpstreamModel = selection.Model
		record.ReasoningEffort = selection.ReasoningEffort
		rewriteReserved := requestRewriteRequired(body, model, selection.Model, selection.ReasoningEffort)
		attemptReservation := rewriteBodyReservation(state.cfg.Server.MaxBodyBytes)
		if rewriteReserved && !bodyLease.Acquire(attemptReservation) {
			selection.Release()
			releaseRetainedBody()
			w.Header().Set("Retry-After", "1")
			writeProtocolError(w, operation, http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
			record.Status = http.StatusTooManyRequests
			record.Outcome = "gateway_rejected"
			record.Error = "request rewrite memory budget reached"
			return
		}
		attemptBody, err := rewriteRequestBody(body, envelope, model, selection.Model, selection.ReasoningEffort)
		if err != nil {
			bodyLease.ShrinkTo(baseBodyReservation)
			selection.Release()
			releaseRetainedBody()
			writeError(http.StatusInternalServerError, "failed to rewrite request", "gateway_error", "gateway_error")
			record.Status = 500
			record.Error = err.Error()
			return
		}
		if rewriteReserved {
			if int64(len(attemptBody)) > attemptReservation {
				bodyLease.ShrinkTo(baseBodyReservation)
				selection.Release()
				releaseRetainedBody()
				writeProtocolError(w, operation, http.StatusRequestEntityTooLarge, "rewritten request body too large", "invalid_request_error", "request_too_large")
				record.Status = http.StatusRequestEntityTooLarge
				record.Outcome = "gateway_rejected"
				return
			}
			bodyLease.ShrinkTo(baseBodyReservation + int64(len(attemptBody)))
		}
		attemptStart := time.Now()
		attemptHealth := selection.Account.beginAttempt(operation, selection.Model, selection.Credential)
		resp, err := g.doUpstream(r.Context(), state, selection.Account, r, attemptBody, requestID, selection.Credential)
		bodyLease.ShrinkTo(baseBodyReservation)
		if err != nil {
			selection.Release()
			if r.Context().Err() != nil {
				record.Status = 499
				record.Error = r.Context().Err().Error()
				releaseRetainedBody()
				return
			}
			safeError := upstreamErrorMessage(err)
			selection.Account.reportFailure(attemptHealth, safeError, state.cfg.Server.FailureThreshold, state.cfg.Server.CircuitCooldown.Duration, false)
			retrySafe := retryableTransportError(err)
			if retrySafe {
				record.Error = safeError
			} else {
				record.Error = "submission outcome uncertain: " + safeError
			}
			if retrySafe && attempt+1 < maxAttempts {
				g.stats.Failover()
				continue
			}
			if !retrySafe {
				releaseRetainedBody()
				writeError(http.StatusBadGateway, "upstream submission outcome is uncertain; request was not retried", "upstream_error", "uncertain_submission")
				record.Status = http.StatusBadGateway
				record.Outcome = "uncertain_submission"
				return
			}
			if last != nil {
				releaseRetainedBody()
				_ = last.write(w, state.cfg.Server.StreamIdleTimeout.Duration)
				record.AccountID = lastAccountID
				record.CredentialID = lastCredentialID
				record.UpstreamModel = lastUpstreamModel
				record.ReasoningEffort = lastReasoningEffort
				record.Status = last.status
				record.Error = fmt.Sprintf("upstream returned %d; final failover failed: %s", last.status, safeError)
				applyBufferedResponseMetadata(&record, last.header, last.body)
				if record.OutputType == "" && operation == config.OperationImages {
					record.OutputType = "image"
				}
				return
			}
			releaseRetainedBody()
			writeError(http.StatusBadGateway, "upstream request failed", "upstream_error", "upstream_error")
			record.Status = http.StatusBadGateway
			return
		}
		if selectedCredential := strings.TrimSpace(resp.Header.Get(credentialSelectedHeader)); selectedCredential != "" {
			record.CredentialID = selectedCredential
		}
		statusRetryable, uncertainStatus := retryableStatusForOperation(operation, resp.StatusCode)
		retrySafeCredentialFailure := selection.Credential != "" &&
			strings.EqualFold(strings.TrimSpace(selection.Account.Config.AdapterID), "cli-proxy-api") &&
			strings.EqualFold(strings.TrimSpace(resp.Header.Get(credentialRetrySafeHeader)), "true")
		if retrySafeCredentialFailure && retryableStatus(resp.StatusCode) {
			statusRetryable, uncertainStatus = true, false
		}
		targetedMissingRetryable := selection.Targeted && resp.StatusCode == http.StatusNotFound
		if uncertainStatus {
			_ = resp.Body.Close()
			selection.Release()
			releaseRetainedBody()
			selection.Account.reportFailure(attemptHealth, "HTTP "+strconv.Itoa(resp.StatusCode), state.cfg.Server.FailureThreshold, state.cfg.Server.CircuitCooldown.Duration, false)
			writeError(http.StatusBadGateway, "upstream submission outcome is uncertain; request was not retried", "upstream_error", "uncertain_submission")
			record.Status = http.StatusBadGateway
			record.Outcome = "uncertain_submission"
			record.Error = "submission outcome uncertain: upstream returned " + strconv.Itoa(resp.StatusCode)
			return
		}
		if statusRetryable || targetedMissingRetryable {
			bufferIdle := min(state.cfg.Server.StreamIdleTimeout.Duration, 5*time.Second)
			buffered, bufferErr := bufferResponse(resp, 1<<20, bufferIdle, 30*time.Second)
			if bufferErr != nil {
				buffered.body = nil
				buffered.readError = truncate(bufferErr.Error(), 1024)
			}
			selection.Release()
			last = buffered
			lastAccountID = selection.Account.Config.ID
			lastCredentialID = record.CredentialID
			lastUpstreamModel = selection.Model
			lastReasoningEffort = selection.ReasoningEffort
			cooldown := cooldownFor(resp, state.cfg.Server.CircuitCooldown.Duration)
			forceCircuit := resp.StatusCode == 401 || resp.StatusCode == 402 || resp.StatusCode == 403
			if !(selection.Targeted && resp.StatusCode == http.StatusNotFound) {
				selection.Account.reportFailure(attemptHealth, "HTTP "+strconv.Itoa(resp.StatusCode), state.cfg.Server.FailureThreshold, cooldown, forceCircuit)
			}
			if attempt+1 < maxAttempts {
				g.stats.Failover()
				continue
			}
			releaseRetainedBody()
			_ = buffered.write(w, state.cfg.Server.StreamIdleTimeout.Duration)
			record.Status = buffered.status
			record.Error = "upstream returned " + strconv.Itoa(buffered.status)
			if buffered.readError != "" {
				record.Error += "; response body unavailable: " + buffered.readError
			}
			applyBufferedResponseMetadata(&record, buffered.header, buffered.body)
			if record.OutputType == "" && operation == config.OperationImages {
				record.OutputType = "image"
			}
			return
		}
		record.Status = resp.StatusCode
		record.Error = ""
		ok = resp.StatusCode < 400
		capture := newResponseCapture(resp.Body, resp.Header.Get("Content-Type"))
		resp.Body = capture
		// No branch after this point can replay the request. Drop every handler
		// reference and release the byte budget before a potentially hours-long
		// response stream.
		body = nil
		envelope = nil
		attemptBody = nil
		releaseRetainedBody()
		streamErr := func() error {
			defer selection.Release()
			return streamResponse(w, resp, state.cfg.Server.StreamIdleTimeout.Duration)
		}()
		applyCapturedResponseMetadata(&record, capture)
		if record.OutputType == "" && operation == config.OperationImages {
			record.OutputType = "image"
		}
		if streamErr != nil {
			ok = false
			record.Error = streamErr.Error()
			var transferErr *streamTransferError
			if errors.As(streamErr, &transferErr) && transferErr.source == streamFailureDownstream {
				var networkErr net.Error
				if errors.As(streamErr, &networkErr) && networkErr.Timeout() {
					record.Outcome = "downstream_timeout"
				} else {
					record.Outcome = "client_cancelled"
				}
			} else if r.Context().Err() == nil {
				selection.Account.reportFailure(attemptHealth, streamErr.Error(), state.cfg.Server.FailureThreshold, state.cfg.Server.CircuitCooldown.Duration, false)
			}
			return
		}
		if resp.StatusCode >= http.StatusBadRequest {
			if retryableStatus(resp.StatusCode) {
				cooldown := cooldownFor(resp, state.cfg.Server.CircuitCooldown.Duration)
				forceCircuit := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusForbidden
				record.Error = "upstream returned " + strconv.Itoa(resp.StatusCode)
				selection.Account.reportFailure(attemptHealth, "HTTP "+strconv.Itoa(resp.StatusCode), state.cfg.Server.FailureThreshold, cooldown, forceCircuit)
				return
			}
			// A non-retryable client/status response is a completed upstream
			// attempt, but it is neither evidence of account health nor a breaker
			// failure. In particular, a client 400 must not close a newer circuit.
			record.Error = "upstream returned " + strconv.Itoa(resp.StatusCode)
			record.Outcome = "upstream_rejected"
			selection.Account.reportNeutral(attemptHealth)
			return
		}
		selection.Account.reportSuccess(attemptHealth, time.Since(attemptStart))
		return
	}
}

func requestOutcome(ok bool, record RequestRecord, contextErr error) string {
	if ok {
		return "success"
	}
	if contextErr != nil || record.Status == 499 {
		return "client_cancelled"
	}
	if strings.Contains(record.Error, "submission outcome uncertain") {
		return "uncertain_submission"
	}
	if record.Error != "" && record.Status >= 200 && record.Status < 400 {
		return "stream_error"
	}
	if record.AccountID == "" {
		if record.Error == ErrNoCapacity.Error() {
			return "queue_timeout"
		}
		return "gateway_rejected"
	}
	if record.Status == http.StatusBadGateway && record.Error != "" {
		return "connect_error"
	}
	if record.Status >= 400 {
		return "upstream_status"
	}
	return "connect_error"
}

func (g *Gateway) tryAcquireGlobal(limit int64) bool {
	if limit <= 0 {
		g.globalActive.Add(1)
		return true
	}
	for {
		current := g.globalActive.Load()
		if current >= limit {
			return false
		}
		if g.globalActive.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (g *Gateway) tryAcquireBodyBytes(reservation, limit int64) bool {
	if reservation < 0 {
		return false
	}
	if reservation == 0 {
		return true
	}
	for {
		current := g.bodyBytes.Load()
		if current > limit-reservation {
			return false
		}
		if g.bodyBytes.CompareAndSwap(current, current+reservation) {
			return true
		}
	}
}

func rewriteBodyReservation(maxBody int64) int64 {
	if maxBody <= 0 {
		maxBody = config.DefaultMaxBodyBytes
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if maxBody > maxInt64-maxInternalRewriteGrowth {
		return maxInt64
	}
	return maxBody + maxInternalRewriteGrowth
}

// requestBodyMemoryBudget covers the three simultaneously retained forms at
// the worst legal point: inbound bytes, decoded RawMessages, and one rewritten
// upstream body. MaxBodyBytes remains the per-request input contract; the
// process-wide accounting envelope must not silently cut it in half.
func requestBodyMemoryBudget(maxBody int64) int64 {
	if maxBody <= 0 {
		maxBody = config.DefaultMaxBodyBytes
	}
	rewrite := rewriteBodyReservation(maxBody)
	const maxInt64 = int64(^uint64(0) >> 1)
	// A fast-profile route can normalize once and then rewrite the selected
	// provider model, so both internal encodings may consume the bounded growth
	// allowance at the same time.
	if rewrite == maxInt64 || rewrite > maxInt64-maxInternalRewriteGrowth || maxBody > (maxInt64-rewrite-maxInternalRewriteGrowth)/2 {
		return maxInt64
	}
	return max(int64(defaultBufferedBodyBudget), 2*maxBody+rewrite+maxInternalRewriteGrowth)
}

type bodyByteLease struct {
	gateway  *Gateway
	limit    int64
	reserved int64
	released bool
}

func (l *bodyByteLease) Acquire(bytes int64) bool {
	if l == nil || bytes <= 0 {
		return true
	}
	if l.released || !l.gateway.tryAcquireBodyBytes(bytes, l.limit) {
		return false
	}
	l.reserved += bytes
	return true
}

func (l *bodyByteLease) ShrinkTo(bytes int64) {
	if l == nil || l.released || bytes >= l.reserved {
		return
	}
	l.gateway.bodyBytes.Add(-(l.reserved - bytes))
	l.reserved = bytes
}

func (l *bodyByteLease) Release() {
	if l == nil || l.released {
		return
	}
	l.released = true
	if l.reserved > 0 {
		l.gateway.bodyBytes.Add(-l.reserved)
		l.reserved = 0
	}
}

// ownedRequestBody makes transport ownership explicit. Client.Do is allowed
// to return an error before the RoundTripper asynchronously closes Body; the
// gateway must not uncharge or reuse the backing attempt bytes before done.
type ownedRequestBody struct {
	mu     sync.Mutex
	reader *bytes.Reader
	done   chan struct{}
	once   sync.Once
}

func newOwnedRequestBody(body []byte) *ownedRequestBody {
	return &ownedRequestBody{reader: bytes.NewReader(body), done: make(chan struct{})}
}

func (b *ownedRequestBody) Read(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reader == nil {
		return 0, io.ErrClosedPipe
	}
	return b.reader.Read(data)
}

func (b *ownedRequestBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.reader = nil
		b.mu.Unlock()
		close(b.done)
	})
	return nil
}

func awaitRequestBodyClose(body *ownedRequestBody, contextDone <-chan struct{}) {
	select {
	case <-body.done:
		return
	case <-contextDone:
		// Bound a non-conforming RoundTripper that never closes Body. Close is
		// concurrency-safe and transfers ownership back before accounting drops.
		_ = body.Close()
		<-body.done
	}
}

func (g *Gateway) doUpstream(ctx context.Context, state *runtimeState, account *AccountRuntime, inbound *http.Request, body []byte, requestID string, credential ...string) (*http.Response, error) {
	upstreamURL, err := buildUpstreamURL(account.Config.BaseURL, inbound.URL.Path, inbound.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	parentContext := ctx
	requestContext, cancelRequest := context.WithCancel(parentContext)
	headerTimeout := state.cfg.Server.ResponseHeaderTimeout.Duration
	if headerTimeout <= 0 {
		headerTimeout = 5 * time.Minute
	}
	timeoutFired := atomic.Bool{}
	timer := time.AfterFunc(headerTimeout, func() {
		timeoutFired.Store(true)
		cancelRequest()
	})
	var requestWritten atomic.Bool
	requestWriteDone := make(chan struct{})
	var requestWriteOnce sync.Once
	trace := &httptrace.ClientTrace{
		// Once headers start crossing the wire, any transport timeout is an
		// uncertain submission even if WroteRequest has not fired yet.
		WroteHeaders: func() { requestWritten.Store(true) },
		WroteRequest: func(httptrace.WroteRequestInfo) {
			requestWritten.Store(true)
			requestWriteOnce.Do(func() { close(requestWriteDone) })
		},
	}
	requestContext = httptrace.WithClientTrace(requestContext, trace)
	ctx = requestContext
	requestBody := newOwnedRequestBody(body)
	req, err := http.NewRequestWithContext(ctx, inbound.Method, upstreamURL, requestBody)
	if err != nil {
		timer.Stop()
		cancelRequest()
		_ = requestBody.Close()
		return nil, err
	}
	// Passing a custom ownership-tracked Body prevents net/http from inferring
	// length as it would for *bytes.Reader. Preserve fixed-length JSON framing;
	// many upstream proxies reject chunked request bodies.
	req.ContentLength = int64(len(body))
	copyRequestHeaders(req.Header, inbound.Header)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	key := account.UpstreamKey
	authHeader := strings.TrimSpace(account.Config.AuthHeader)
	if !strings.EqualFold(authHeader, "none") {
		value := key
		if scheme := strings.TrimSpace(account.Config.AuthScheme); scheme != "" {
			value = scheme + " " + key
		}
		req.Header.Set(authHeader, value)
	}
	for name, value := range account.CustomHeaders {
		req.Header.Set(name, value)
	}
	if strings.EqualFold(strings.TrimSpace(account.Config.AdapterID), "cli-proxy-api") && len(credential) > 0 {
		if publicID := strings.TrimSpace(credential[0]); publicID != "" {
			req.Header.Set(credentialPinHeader, publicID)
		}
	}
	response, err := state.clients[account.Config.ID].Do(req)
	if err != nil {
		awaitRequestBodyClose(requestBody, requestContext.Done())
		timer.Stop()
		cancelRequest()
		if timeoutFired.Load() && parentContext.Err() == nil {
			err = fmt.Errorf("upstream request/header timeout after %s: %w", headerTimeout, context.DeadlineExceeded)
		}
		return nil, &upstreamRequestError{err: err, wroteRequest: requestWritten.Load()}
	}
	// Client.Do may expose early response headers while its transport is still
	// finishing or aborting the upload. Do not let the caller release the body
	// memory lease until WroteRequest confirms that transport ownership ended.
	select {
	case <-requestWriteDone:
	case <-requestBody.done:
	case <-requestContext.Done():
		_ = response.Body.Close()
		_ = requestBody.Close()
		timer.Stop()
		cancelRequest()
		err := requestContext.Err()
		if timeoutFired.Load() && parentContext.Err() == nil {
			err = fmt.Errorf("upstream request/header timeout after %s: %w", headerTimeout, context.DeadlineExceeded)
		}
		return nil, &upstreamRequestError{err: err, wroteRequest: requestWritten.Load()}
	}
	awaitRequestBodyClose(requestBody, requestContext.Done())
	if !timer.Stop() || requestContext.Err() != nil {
		_ = response.Body.Close()
		_ = requestBody.Close()
		cancelRequest()
		err := requestContext.Err()
		if timeoutFired.Load() && parentContext.Err() == nil {
			err = fmt.Errorf("upstream request/header timeout after %s: %w", headerTimeout, context.DeadlineExceeded)
		}
		return nil, &upstreamRequestError{err: err, wroteRequest: requestWritten.Load()}
	}
	// A response retains its originating Request. Detach the consumed body so a
	// long response cannot keep a rewritten multi-megabyte slice alive after
	// its accounting lease is reduced or released.
	req.Body = nil
	req.GetBody = nil
	response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancelRequest}
	return response, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

type upstreamRequestError struct {
	err          error
	wroteRequest bool
}

func (e *upstreamRequestError) Error() string { return e.err.Error() }
func (e *upstreamRequestError) Unwrap() error { return e.err }

func (g *Gateway) serveModels(w http.ResponseWriter, state *runtimeState, lease *KeyLease) {
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(state.models))
	for _, model := range state.models {
		if !lease.AllowsModel(model) {
			continue
		}
		data = append(data, map[string]any{"id": model, "object": "model", "created": now, "owned_by": "lite2api"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func operationForGatewayPath(path string) (string, bool) {
	switch strings.TrimSuffix(path, "/") {
	case "/v1/chat/completions":
		return config.OperationOpenAIChat, true
	case "/v1/responses":
		return config.OperationOpenAIResponses, true
	case "/v1/messages":
		return config.OperationAnthropic, true
	case "/v1/embeddings":
		return config.OperationEmbeddings, true
	case "/v1/images/generations":
		return config.OperationImages, true
	case "/v1/rerank":
		return config.OperationRerank, true
	default:
		return "", false
	}
}

func validateTopLevelEnvelope(data []byte) error {
	index := skipJSONWhitespace(data, 0)
	if index >= len(data) || data[index] != '{' {
		return errInvalidEnvelopeShape
	}
	index = skipJSONWhitespace(data, index+1)
	if index < len(data) && data[index] == '}' {
		index = skipJSONWhitespace(data, index+1)
		if index != len(data) {
			return errInvalidEnvelopeShape
		}
		return nil
	}
	fields := 0
	for {
		if index >= len(data) || data[index] != '"' {
			return errInvalidEnvelopeShape
		}
		keyStart := index
		keyEnd, err := scanJSONString(data, index)
		if err != nil {
			return errInvalidEnvelopeShape
		}
		// Escaped JSON may use six source bytes for one decoded key byte. The
		// exact 256-byte contract is enforced after bounded-cardinality Unmarshal.
		if keyEnd-keyStart-2 > maxRequestEnvelopeKeyBytes*6 {
			return errRequestFieldTooLong
		}
		fields++
		if fields > maxRequestEnvelopeFields {
			return errTooManyRequestFields
		}
		index = skipJSONWhitespace(data, keyEnd)
		if index >= len(data) || data[index] != ':' {
			return errInvalidEnvelopeShape
		}
		index = skipJSONWhitespace(data, index+1)
		index, err = skipJSONValue(data, index)
		if err != nil {
			return errInvalidEnvelopeShape
		}
		index = skipJSONWhitespace(data, index)
		if index >= len(data) {
			return errInvalidEnvelopeShape
		}
		switch data[index] {
		case ',':
			index = skipJSONWhitespace(data, index+1)
		case '}':
			index = skipJSONWhitespace(data, index+1)
			if index != len(data) {
				return errInvalidEnvelopeShape
			}
			return nil
		default:
			return errInvalidEnvelopeShape
		}
	}
}

func skipJSONWhitespace(data []byte, index int) int {
	for index < len(data) {
		switch data[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func scanJSONString(data []byte, index int) (int, error) {
	if index >= len(data) || data[index] != '"' {
		return index, errInvalidEnvelopeShape
	}
	for index++; index < len(data); index++ {
		switch data[index] {
		case '"':
			return index + 1, nil
		case '\\':
			index++
			if index >= len(data) {
				return index, errInvalidEnvelopeShape
			}
		case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
			16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31:
			return index, errInvalidEnvelopeShape
		}
	}
	return index, errInvalidEnvelopeShape
}

func skipJSONValue(data []byte, index int) (int, error) {
	if index >= len(data) {
		return index, errInvalidEnvelopeShape
	}
	if data[index] == '"' {
		return scanJSONString(data, index)
	}
	if data[index] != '{' && data[index] != '[' {
		start := index
		for index < len(data) {
			switch data[index] {
			case ' ', '\t', '\r', '\n', ',', '}', ']':
				if index == start {
					return index, errInvalidEnvelopeShape
				}
				return index, nil
			default:
				index++
			}
		}
		if index == start {
			return index, errInvalidEnvelopeShape
		}
		return index, nil
	}
	var stack [256]byte
	depth := 1
	stack[0] = data[index]
	for index++; index < len(data); {
		switch data[index] {
		case '"':
			var err error
			index, err = scanJSONString(data, index)
			if err != nil {
				return index, err
			}
		case '{', '[':
			if depth == len(stack) {
				return index, errInvalidEnvelopeShape
			}
			stack[depth] = data[index]
			depth++
			index++
		case '}', ']':
			opening := stack[depth-1]
			if (data[index] == '}' && opening != '{') || (data[index] == ']' && opening != '[') {
				return index, errInvalidEnvelopeShape
			}
			depth--
			index++
			if depth == 0 {
				return index, nil
			}
		default:
			index++
		}
	}
	return index, errInvalidEnvelopeShape
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64, operation string, lease *bodyByteLease) ([]byte, error) {
	if limit <= 0 {
		limit = config.DefaultMaxBodyBytes
	}
	if r.ContentLength > limit {
		writeProtocolError(w, operation, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
		return nil, errors.New("request body too large")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var body bytes.Buffer
	if r.ContentLength > 0 && r.ContentLength <= limit {
		body.Grow(int(r.ContentLength))
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := r.Body.Read(buffer)
		if n > 0 {
			nextSize := int64(body.Len() + n)
			if lease != nil && nextSize > lease.reserved && !lease.Acquire(nextSize-lease.reserved) {
				_ = r.Body.Close()
				w.Header().Set("Retry-After", "1")
				writeProtocolError(w, operation, http.StatusTooManyRequests, "gateway request-body memory budget reached", "rate_limit_error", "body_memory_limit_reached")
				return nil, errors.New("request body memory budget reached")
			}
			_, _ = body.Write(buffer[:n])
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if lease != nil {
				lease.ShrinkTo(int64(body.Len()))
			}
			return body.Bytes(), nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(readErr, &tooLarge) {
			writeProtocolError(w, operation, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
		} else {
			writeProtocolError(w, operation, http.StatusBadRequest, "unable to read request body", "invalid_request_error", "invalid_request_body")
		}
		return nil, readErr
	}
}

func rewriteRequest(envelope map[string]json.RawMessage, model, reasoningEffort string) ([]byte, error) {
	return rewriteRequestBody(nil, envelope, "", model, reasoningEffort)
}

func requestRewriteRequired(original []byte, requestedModel, model, reasoningEffort string) bool {
	return !(len(original) > 0 && model == requestedModel && (reasoningEffort == "" || reasoningEffort == "auto"))
}

func rewriteRequestBody(original []byte, envelope map[string]json.RawMessage, requestedModel, model, reasoningEffort string) ([]byte, error) {
	if !requestRewriteRequired(original, requestedModel, model, reasoningEffort) {
		return original, nil
	}
	copyEnvelope := make(map[string]json.RawMessage, len(envelope))
	for key, value := range envelope {
		copyEnvelope[key] = value
	}
	encoded, _ := json.Marshal(model)
	copyEnvelope["model"] = encoded
	switch reasoningEffort {
	case "", "auto":
		// Preserve a client-supplied value when the target delegates reasoning.
	case "none":
		delete(copyEnvelope, "reasoning_effort")
	default:
		encodedEffort, _ := json.Marshal(reasoningEffort)
		copyEnvelope["reasoning_effort"] = encodedEffort
	}
	return json.Marshal(copyEnvelope)
}

func sessionKey(r *http.Request, body map[string]json.RawMessage) (string, error) {
	for _, header := range []string{"X-Session-Id", "Session-Id", "Conversation-Id"} {
		raw := r.Header.Get(header)
		if len(raw) > maxSessionKeyBytes {
			return "", errSessionKeyTooLong
		}
		if value := strings.TrimSpace(raw); value != "" {
			return value, nil
		}
	}
	for _, key := range []string{"prompt_cache_key", "user", "previous_response_id"} {
		raw := body[key]
		// A JSON string can use six input bytes per decoded code point. Bound the
		// encoded form before Unmarshal so a 64MiB optional field never creates a
		// fourth request-sized allocation.
		if len(raw) > maxSessionKeyBytes*6+2 {
			return "", errSessionKeyTooLong
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && value != "" {
			if len(value) > maxSessionKeyBytes {
				return "", errSessionKeyTooLong
			}
			return value, nil
		}
	}
	return "", nil
}

func requestID() string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(data[:])
}

func buildUpstreamURL(base, requestPath, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimSuffix(u.Path, "/")
	path := requestPath
	if (strings.HasSuffix(basePath, "/v1") || strings.HasSuffix(basePath, "/openai")) && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	u.Path = basePath + "/" + strings.TrimPrefix(path, "/")
	// Provider-required query parameters (for example Azure api-version) are
	// part of the configured endpoint identity and cannot be overridden by an
	// inbound client. Non-conflicting inbound parameters are preserved.
	query := u.Query()
	inbound, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid request query: %w", err)
	}
	for name, values := range inbound {
		if query.Has(name) {
			continue
		}
		for _, value := range values {
			query.Add(name, value)
		}
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

const (
	credentialPinHeader       = "X-Lite2API-Auth-Index"
	credentialSelectedHeader  = "X-Lite2API-Selected-Auth-Index"
	credentialRetrySafeHeader = "X-Lite2API-Retry-Safe"
)

var hopHeaders = map[string]struct{}{"connection": {}, "proxy-connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {}, "te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {}, "authorization": {}, "x-api-key": {}, "cookie": {}, "host": {}, strings.ToLower(credentialPinHeader): {}, strings.ToLower(credentialSelectedHeader): {}, strings.ToLower(credentialRetrySafeHeader): {}}

func copyRequestHeaders(dst, src http.Header) {
	connectionBlocked := connectionHeaderNames(src)
	for name, values := range src {
		lower := strings.ToLower(name)
		if _, blocked := hopHeaders[lower]; blocked {
			continue
		}
		if _, blocked := connectionBlocked[lower]; blocked {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	connectionBlocked := connectionHeaderNames(src)
	for name, values := range src {
		lower := strings.ToLower(name)
		if _, blocked := hopHeaders[lower]; blocked || lower == "set-cookie" {
			continue
		}
		if _, blocked := connectionBlocked[lower]; blocked {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func connectionHeaderNames(header http.Header) map[string]struct{} {
	blocked := make(map[string]struct{})
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
				blocked[token] = struct{}{}
			}
		}
	}
	return blocked
}

func streamResponse(w http.ResponseWriter, resp *http.Response, idleTimeout time.Duration) error {
	if idleTimeout <= 0 {
		idleTimeout = 15 * time.Minute
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	type readEvent struct {
		data []byte
		err  error
	}
	events := make(chan readEvent)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buffer)
			event := readEvent{err: err}
			if n > 0 {
				event.data = append([]byte(nil), buffer[:n]...)
			}
			if n > 0 || err != nil {
				select {
				case events <- event:
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		close(stop)
		_ = resp.Body.Close()
		// No goroutine that owns resp.Body may outlive the handler. In
		// particular, the reader never receives the ResponseWriter, so a slow
		// downstream cannot be confused with upstream inactivity.
		<-done
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if len(event.data) > 0 {
				// Pause upstream-idle accounting during the potentially blocking
				// downstream write. Slow client backpressure is not upstream idle.
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if err := writeDownstreamChunk(w, event.data, min(idleTimeout, 30*time.Second)); err != nil {
					return &streamTransferError{source: streamFailureDownstream, err: err}
				}
				timer.Reset(idleTimeout)
			}
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return nil
				}
				return &streamTransferError{source: streamFailureUpstream, err: event.err}
			}
		case <-timer.C:
			_ = resp.Body.Close()
			return &streamTransferError{source: streamFailureUpstream, err: fmt.Errorf("upstream stream idle for %s", idleTimeout)}
		}
	}
}

type streamFailureSource uint8

const (
	streamFailureUpstream streamFailureSource = iota + 1
	streamFailureDownstream
)

type streamTransferError struct {
	source streamFailureSource
	err    error
}

func (e *streamTransferError) Error() string { return e.err.Error() }
func (e *streamTransferError) Unwrap() error { return e.err }

func writeDownstreamChunk(w http.ResponseWriter, data []byte, timeout time.Duration) error {
	timeout = boundedDownstreamWriteTimeout(timeout)
	controller := http.NewResponseController(w)
	deadlineSupported := false
	if err := controller.SetWriteDeadline(time.Now().Add(timeout)); err == nil {
		deadlineSupported = true
	} else if !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set downstream write deadline: %w", err)
	}
	if deadlineSupported {
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	n, err := w.Write(data)
	if err != nil {
		return fmt.Errorf("write downstream response: %w", err)
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("flush downstream response: %w", err)
	}
	return nil
}

func boundedDownstreamWriteTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > 30*time.Second {
		return 30 * time.Second
	}
	return timeout
}

type bufferedResponse struct {
	status    int
	header    http.Header
	body      []byte
	readError string
}

func bufferResponse(resp *http.Response, limit int64, idleTimeout, totalTimeout time.Duration) (*bufferedResponse, error) {
	result := &bufferedResponse{status: resp.StatusCode, header: resp.Header.Clone()}
	if limit <= 0 {
		_ = resp.Body.Close()
		return result, nil
	}
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Second
	}
	if totalTimeout <= 0 {
		totalTimeout = 30 * time.Second
	}
	type readEvent struct {
		data []byte
		err  error
	}
	events := make(chan readEvent)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := io.LimitReader(resp.Body, limit)
		buffer := make([]byte, 32<<10)
		for {
			n, err := reader.Read(buffer)
			event := readEvent{err: err}
			if n > 0 {
				event.data = append([]byte(nil), buffer[:n]...)
			}
			if n > 0 || err != nil {
				select {
				case events <- event:
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		close(stop)
		_ = resp.Body.Close()
		<-done
	}()
	idleTimer := time.NewTimer(idleTimeout)
	totalTimer := time.NewTimer(totalTimeout)
	defer idleTimer.Stop()
	defer totalTimer.Stop()
	for {
		select {
		case event := <-events:
			if len(event.data) > 0 {
				result.body = append(result.body, event.data...)
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(idleTimeout)
			}
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return result, nil
				}
				return result, fmt.Errorf("read upstream error response: %w", event.err)
			}
		case <-idleTimer.C:
			return result, fmt.Errorf("upstream error response idle for %s", idleTimeout)
		case <-totalTimer.C:
			return result, fmt.Errorf("upstream error response exceeded %s", totalTimeout)
		}
	}
}
func (r *bufferedResponse) write(w http.ResponseWriter, timeout time.Duration) error {
	controller := http.NewResponseController(w)
	timeout = boundedDownstreamWriteTimeout(timeout)
	deadlineSupported := controller.SetWriteDeadline(time.Now().Add(timeout)) == nil
	if deadlineSupported {
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	copyResponseHeaders(w.Header(), r.header)
	w.Header().Del("Content-Length")
	w.WriteHeader(r.status)
	n, err := w.Write(r.body)
	if err != nil {
		return fmt.Errorf("write buffered downstream response: %w", err)
	}
	if n != len(r.body) {
		return io.ErrShortWrite
	}
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("flush buffered downstream response: %w", err)
	}
	return nil
}

func retryableStatus(code int) bool {
	return code == 401 || code == 402 || code == 403 || code == 408 || code == 409 || code == 425 || code == 429 || (code >= 500 && code <= 599)
}

// retryableStatusForOperation only fails over after a definitive provider
// rejection. 408/409/425/5xx remain uncertain for every POST operation: the
// provider may already have accepted and billed the work, and an inbound
// idempotency key does not prove cross-provider deduplication.
func retryableStatusForOperation(operation string, code int) (retry, uncertain bool) {
	_ = operation
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true, false
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly:
		return false, true
	}
	if code >= 500 && code <= 599 {
		return false, true
	}
	return false, false
}

// retryableTransportError only permits failover when the request could not
// have reached an upstream HTTP server. Timeouts and connection resets after
// a write have an uncertain submission outcome and must not duplicate a
// billable generation on another provider.
func retryableTransportError(err error) bool {
	var requestError *upstreamRequestError
	if errors.As(err, &requestError) {
		return !requestError.wroteRequest
	}
	var operationError *net.OpError
	return errors.As(err, &operationError) && operationError.Op == "dial"
}

func upstreamErrorMessage(err error) string {
	var requestURL *url.Error
	if errors.As(err, &requestURL) {
		redactedURL := requestURL.URL
		if parsed, parseErr := url.Parse(requestURL.URL); parseErr == nil {
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			parsed.User = nil
			redactedURL = parsed.String()
		}
		return truncate(fmt.Sprintf("%s %s: %v", requestURL.Op, redactedURL, requestURL.Err), 1024)
	}
	return truncate(err.Error(), 1024)
}
func cooldownFor(resp *http.Response, fallback time.Duration) time.Duration {
	const maximum = 24 * time.Hour
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		if seconds >= int64(maximum/time.Second) {
			return maximum
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil {
		wait := time.Until(retryAt)
		if wait > 0 {
			return min(wait, maximum)
		}
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeAPIError(w http.ResponseWriter, status int, message, kind string) {
	writeAPIErrorCode(w, status, message, kind, kind)
}
func writeAPIErrorCode(w http.ResponseWriter, status int, message, kind, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "param": nil, "code": code}})
}

func writeProtocolError(w http.ResponseWriter, operation string, status int, message, kind, code string) {
	if operation != config.OperationAnthropic {
		writeAPIErrorCode(w, status, message, kind, code)
		return
	}
	responseType := "api_error"
	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed:
		responseType = "invalid_request_error"
	case http.StatusUnauthorized:
		responseType = "authentication_error"
	case http.StatusForbidden:
		responseType = "permission_error"
	case http.StatusNotFound:
		responseType = "not_found_error"
	case http.StatusRequestEntityTooLarge:
		responseType = "request_too_large"
	case http.StatusTooManyRequests:
		responseType = "rate_limit_error"
	case http.StatusServiceUnavailable:
		responseType = "overloaded_error"
	}
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    responseType,
			"message": message,
		},
	})
}

func (g *Gateway) LogState() {
	state := g.state.Load()
	slog.Info("configuration loaded", "accounts", len(state.cfg.Accounts), "models", len(state.scheduler.Models()))
}
