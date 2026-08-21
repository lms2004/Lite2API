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
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type runtimeState struct {
	cfg             config.Config
	scheduler       *Scheduler
	clients         map[string]*http.Client
	models          []string
	legacyKeyHashes map[[sha256.Size]byte]struct{}
	adminToken      string
	adminAllowed    []*net.IPNet
	trustedProxies  []*net.IPNet
}

type Gateway struct {
	configPath   string
	store        *config.Store
	state        atomic.Pointer[runtimeState]
	requestLog   atomic.Pointer[requestLogWriter]
	reloadMu     sync.Mutex
	stats        *Stats
	globalActive atomic.Int64
	clientKeys   *ClientKeyStore
	adminAuth    *AdminAuthenticator
	adapterProbe *adapterProbeCache
}

func New(configPath string) (*Gateway, error) {
	g := &Gateway{
		configPath: configPath, store: config.NewStore(configPath), stats: NewStats(512),
		adapterProbe: newAdapterProbeCache(time.Minute),
	}
	if err := g.Reload(); err != nil {
		return nil, err
	}
	state := g.state.Load()
	if records, err := loadRequestRecords(resolveRequestLogPath(configPath, state.cfg.Server.RequestLogPath), state.cfg.Server.RequestLogBackups); err != nil {
		slog.Warn("latest request baseline unavailable", "error", err)
	} else {
		// Restore request observations for health/readiness and historical trend
		// rendering. Cumulative counters intentionally start at this process
		// lifetime and are not reconstructed from the log.
		for _, record := range records {
			g.stats.Record(record)
		}
	}
	keyPath := ResolveClientKeysPath(configPath, state.cfg.Server.ClientKeysPath)
	clientKeys, err := NewClientKeyStore(keyPath)
	if err != nil {
		return nil, err
	}
	g.clientKeys = clientKeys
	g.adminAuth = NewAdminAuthenticator(state.cfg.Server.AdminSessionTTL.Duration)
	return g, nil
}

func (g *Gateway) Reload() error {
	g.reloadMu.Lock()
	defer g.reloadMu.Unlock()
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	state, err := g.buildState(cfg)
	if err != nil {
		return err
	}
	if g.clientKeys != nil {
		keyPath := ResolveClientKeysPath(g.configPath, state.cfg.Server.ClientKeysPath)
		if filepath.Clean(keyPath) != filepath.Clean(g.clientKeys.Path()) {
			return errors.New("server.client_keys_path cannot change during hot reload")
		}
	}
	if g.adminAuth != nil {
		g.adminAuth.SetTTL(state.cfg.Server.AdminSessionTTL.Duration)
	}
	requestLog, err := newRequestLogWriter(
		resolveRequestLogPath(g.configPath, state.cfg.Server.RequestLogPath),
		state.cfg.Server.RequestLogMaxBytes,
		state.cfg.Server.RequestLogBackups,
	)
	if err != nil {
		return fmt.Errorf("request log: %w", err)
	}
	g.swapState(state)
	oldLog := g.requestLog.Swap(requestLog)
	if oldLog != nil {
		oldLog.Close()
	}
	return nil
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

func (g *Gateway) Config() config.Config       { return g.state.Load().cfg }
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
	lease, authFailure := g.clientKeys.Authenticate(apiBearerToken(r), state.legacyKeyHashes)
	switch authFailure {
	case KeyAuthInvalid:
		writeAPIErrorCode(w, http.StatusUnauthorized, "invalid API key", "authentication_error", "invalid_api_key")
		return
	case KeyAuthRateLimited:
		w.Header().Set("Retry-After", "60")
		writeAPIErrorCode(w, http.StatusTooManyRequests, "API key rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		return
	case KeyAuthConcurrency:
		w.Header().Set("Retry-After", "1")
		writeAPIErrorCode(w, http.StatusTooManyRequests, "API key concurrency limit reached", "rate_limit_error", "concurrency_limit_exceeded")
		return
	}
	ok := false
	defer func() { lease.Complete(ok) }()
	if r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/v1/models" {
		g.serveModels(w, state, lease)
		ok = true
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	operation, supported := operationForGatewayPath(r.URL.Path)
	if !supported {
		writeAPIError(w, http.StatusNotFound, "unsupported endpoint", "invalid_request_error")
		return
	}
	if !g.tryAcquireGlobal(int64(state.cfg.Server.MaxInFlightRequests)) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, "gateway concurrency limit reached", "rate_limit_error")
		return
	}
	defer g.globalActive.Add(-1)

	body, err := readBody(w, r, state.cfg.Server.MaxBodyBytes)
	if err != nil {
		return
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeAPIError(w, http.StatusBadRequest, "request body must be valid JSON", "invalid_request_error")
		return
	}
	var model string
	_ = json.Unmarshal(envelope["model"], &model)
	if model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "invalid_request_error")
		return
	}
	if !lease.AllowsModel(model) {
		writeAPIErrorCode(w, http.StatusForbidden, "model is not allowed for this API key", "permission_error", "model_not_allowed")
		return
	}
	session := sessionKey(r, envelope)
	requestID := requestID()
	start := time.Now()
	g.stats.Begin()
	input := inspectRequest(envelope, operation)
	var stream bool
	_ = json.Unmarshal(envelope["stream"], &stream)
	record := RequestRecord{
		Operation:    operation,
		InputType:    input.Kind(),
		InputParts:   input.Total(),
		TextParts:    input.Text,
		ImageParts:   input.Image,
		AudioParts:   input.Audio,
		VideoParts:   input.Video,
		FileParts:    input.File,
		RequestBytes: int64(len(body)),
		Stream:       stream,
	}
	defer func() {
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
		if logger := g.requestLog.Load(); logger != nil {
			logger.Enqueue(record)
		}
	}()

	excluded := make(map[string]struct{})
	var last *bufferedResponse
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
			record.Error = err.Error()
			if last != nil {
				last.write(w)
				record.Status = last.status
				applyBufferedResponseMetadata(&record, last.header, last.body)
				if record.OutputType == "" && operation == config.OperationImages {
					record.OutputType = "image"
				}
				return
			}
			if cached := state.scheduler.CachedUpstreamFailure(model, operation, excluded); cached != nil && cached.Response != nil {
				cached.Response.write(w)
				record.AccountID = cached.AccountID
				record.Status = cached.Response.status
				record.Error = "upstream returned " + strconv.Itoa(cached.Response.status)
				applyBufferedResponseMetadata(&record, cached.Response.header, cached.Response.body)
				if record.OutputType == "" && operation == config.OperationImages {
					record.OutputType = "image"
				}
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, err.Error(), "upstream_unavailable")
			record.Status = http.StatusServiceUnavailable
			return
		}
		excluded[selection.Key] = struct{}{}
		record.AccountID = selection.Account.Config.ID
		record.UpstreamModel = selection.Model
		record.ReasoningEffort = selection.ReasoningEffort
		attemptBody, err := rewriteRequestBody(body, envelope, model, selection.Model, selection.ReasoningEffort)
		if err != nil {
			selection.Release()
			writeAPIError(w, http.StatusInternalServerError, "failed to rewrite request", "gateway_error")
			record.Status = 500
			record.Error = err.Error()
			return
		}
		attemptStart := time.Now()
		resp, err := g.doUpstream(r.Context(), state, selection.Account, r, attemptBody, requestID)
		if err != nil {
			selection.Release()
			if r.Context().Err() != nil {
				record.Status = 499
				record.Error = r.Context().Err().Error()
				return
			}
			selection.Account.reportFailure(err.Error(), state.cfg.Server.FailureThreshold, state.cfg.Server.CircuitCooldown.Duration, false)
			record.Error = err.Error()
			if attempt+1 < maxAttempts {
				g.stats.Failover()
				continue
			}
			writeAPIError(w, http.StatusBadGateway, "upstream request failed", "upstream_error")
			record.Status = http.StatusBadGateway
			return
		}
		if retryableStatus(resp.StatusCode) || (selection.Targeted && resp.StatusCode == http.StatusNotFound) {
			buffered := bufferResponse(resp, 1<<20)
			selection.Release()
			last = buffered
			cooldown := cooldownFor(resp, state.cfg.Server.CircuitCooldown.Duration)
			forceCircuit := resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429
			if !(selection.Targeted && resp.StatusCode == http.StatusNotFound) {
				selection.Account.reportFailure("HTTP "+strconv.Itoa(resp.StatusCode), state.cfg.Server.FailureThreshold, cooldown, forceCircuit)
				if retryableStatus(resp.StatusCode) {
					selection.Account.rememberUpstreamFailure(model, operation, buffered)
				}
			}
			if attempt+1 < maxAttempts {
				g.stats.Failover()
				continue
			}
			buffered.write(w)
			record.Status = buffered.status
			record.Error = "upstream returned " + strconv.Itoa(buffered.status)
			applyBufferedResponseMetadata(&record, buffered.header, buffered.body)
			if record.OutputType == "" && operation == config.OperationImages {
				record.OutputType = "image"
			}
			return
		}
		record.Status = resp.StatusCode
		ok = resp.StatusCode < 400
		capture := newResponseCapture(resp.Body, resp.Header.Get("Content-Type"))
		resp.Body = capture
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
			if r.Context().Err() == nil {
				selection.Account.reportFailure(streamErr.Error(), state.cfg.Server.FailureThreshold, state.cfg.Server.CircuitCooldown.Duration, false)
			}
			return
		}
		selection.Account.reportSuccess(time.Since(attemptStart))
		return
	}
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

func (g *Gateway) doUpstream(ctx context.Context, state *runtimeState, account *AccountRuntime, inbound *http.Request, body []byte, requestID string) (*http.Response, error) {
	upstreamURL, err := buildUpstreamURL(account.Config.BaseURL, inbound.URL.Path, inbound.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, inbound.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
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
	return state.clients[account.Config.ID].Do(req)
}

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

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error")
		return nil, err
	}
	return body, nil
}

func rewriteRequest(envelope map[string]json.RawMessage, model, reasoningEffort string) ([]byte, error) {
	return rewriteRequestBody(nil, envelope, "", model, reasoningEffort)
}

func rewriteRequestBody(original []byte, envelope map[string]json.RawMessage, requestedModel, model, reasoningEffort string) ([]byte, error) {
	if len(original) > 0 && model == requestedModel && (reasoningEffort == "" || reasoningEffort == "auto") {
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

func sessionKey(r *http.Request, body map[string]json.RawMessage) string {
	for _, header := range []string{"X-Session-Id", "Session-Id", "Conversation-Id"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return value
		}
	}
	for _, key := range []string{"prompt_cache_key", "user", "previous_response_id"} {
		var value string
		if json.Unmarshal(body[key], &value) == nil && value != "" {
			return value
		}
	}
	return ""
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
	u.RawQuery = rawQuery
	return u.String(), nil
}

var hopHeaders = map[string]struct{}{"connection": {}, "proxy-connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {}, "te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {}, "authorization": {}, "x-api-key": {}, "cookie": {}, "host": {}}

func copyRequestHeaders(dst, src http.Header) {
	for name, values := range src {
		if _, blocked := hopHeaders[strings.ToLower(name)]; blocked {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		if _, blocked := hopHeaders[lower]; blocked || lower == "set-cookie" {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func streamResponse(w http.ResponseWriter, resp *http.Response, idleTimeout time.Duration) error {
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	writer := io.Writer(w)
	if flusher, ok := w.(http.Flusher); ok {
		writer = flushWriter{w: w, f: flusher}
	}
	if idleTimeout <= 0 {
		_, err := io.CopyBuffer(writer, resp.Body, make([]byte, 32<<10))
		return err
	}
	activity := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := io.CopyBuffer(activityWriter{Writer: writer, activity: activity}, resp.Body, make([]byte, 32<<10))
		done <- err
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		case <-timer.C:
			_ = resp.Body.Close()
			return fmt.Errorf("upstream stream idle for %s", idleTimeout)
		}
	}
}

type activityWriter struct {
	io.Writer
	activity chan<- struct{}
}

func (w activityWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n > 0 {
		select {
		case w.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	w.f.Flush()
	return n, err
}

type bufferedResponse struct {
	status int
	header http.Header
	body   []byte
}

func bufferResponse(resp *http.Response, limit int64) *bufferedResponse {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return &bufferedResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: data}
}
func (r *bufferedResponse) write(w http.ResponseWriter) {
	copyResponseHeaders(w.Header(), r.header)
	w.Header().Del("Content-Length")
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body)
}

func retryableStatus(code int) bool {
	return code == 401 || code == 403 || code == 408 || code == 409 || code == 429 || code == 500 || code == 502 || code == 503 || code == 504
}
func cooldownFor(resp *http.Response, fallback time.Duration) time.Duration {
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds < 3600 {
		return time.Duration(seconds) * time.Second
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

func (g *Gateway) LogState() {
	state := g.state.Load()
	slog.Info("configuration loaded", "accounts", len(state.cfg.Accounts), "models", len(state.scheduler.Models()))
}
