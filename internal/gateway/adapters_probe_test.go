package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdapterProbeSingleflightCoalescesConcurrentCallers(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cache := newAdapterProbeCache(time.Minute)
	item := AdapterDescriptor{ID: "single", LocalURL: server.URL + "/v1"}
	const callers = 32
	start := make(chan struct{})
	results := make(chan adapterProbeResult, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			<-start
			results <- cache.probe(context.Background(), item)
		}()
	}
	close(start)
	<-entered
	close(release)
	group.Wait()
	close(results)
	for result := range results {
		if !result.running || !result.ready {
			t.Fatalf("coalesced result=%+v", result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("network probes=%d, want one singleflight request", got)
	}
}

func TestAdapterProbeSlowFailureDoesNotBlockOtherAdapter(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(release) }) }
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(blocked)
		<-release
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fast.Close()
	defer releaseSlow()

	cache := newAdapterProbeCache(time.Minute)
	cache.client = &http.Client{Timeout: 2 * time.Second}
	slowDone := make(chan adapterProbeResult, 1)
	go func() {
		slowDone <- cache.probe(context.Background(), AdapterDescriptor{ID: "slow", LocalURL: slow.URL + "/v1"})
	}()
	<-blocked
	fastDone := make(chan adapterProbeResult, 1)
	go func() {
		fastDone <- cache.probe(context.Background(), AdapterDescriptor{ID: "fast", LocalURL: fast.URL + "/v1"})
	}()
	select {
	case result := <-fastDone:
		if !result.ready {
			t.Fatalf("fast adapter result=%+v", result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow adapter held a global probe lock and blocked an independent adapter")
	}
	releaseSlow()
	<-slowDone
}

func TestAdapterProbeRequiresSuccessfulReadinessStatus(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		ready        bool
		authRequired bool
	}{
		{name: "success", status: http.StatusNoContent, ready: true},
		{name: "not found", status: http.StatusNotFound},
		{name: "unauthorized", status: http.StatusUnauthorized, authRequired: true},
		{name: "server error", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			result := newAdapterProbeCache(time.Minute).probe(context.Background(), AdapterDescriptor{ID: test.name, LocalURL: server.URL + "/v1"})
			if !result.running || result.ready != test.ready || result.authRequired != test.authRequired {
				t.Fatalf("status %d classified as %+v", test.status, result)
			}
		})
	}
}

func TestInstalledAdapterReadinessRequiresAuthenticatedModelCatalog(t *testing.T) {
	t.Setenv("GROK2API_KEY", "probe-secret")
	var modelStatus atomic.Int64
	modelStatus.Store(http.StatusUnauthorized)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			if got := r.Header.Get("Authorization"); got != "Bearer probe-secret" {
				t.Errorf("authorization=%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(int(modelStatus.Load()))
			if modelStatus.Load() == http.StatusOK {
				_, _ = w.Write([]byte(`{"data":[{"id":"grok-4"}]}`))
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	item := AdapterDescriptor{ID: "grok2api", LocalURL: server.URL + "/v1"}

	unauthorized := newAdapterProbeCache(0).probe(context.Background(), item)
	if unauthorized.ready || !unauthorized.authRequired {
		t.Fatalf("unauthorized model catalog classified as %+v", unauthorized)
	}
	modelStatus.Store(http.StatusOK)
	ready := newAdapterProbeCache(0).probe(context.Background(), item)
	if !ready.ready || ready.authRequired || ready.modelCount != 1 {
		t.Fatalf("authenticated model catalog classified as %+v", ready)
	}
}

func TestAdapterProbeCacheInvalidatesWhenCredentialRotates(t *testing.T) {
	const rotatedKey = "rotated-secret"
	t.Setenv("GROK2API_KEY", "expired-secret")
	var modelCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		modelCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+rotatedKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4"}]}`))
	}))
	defer server.Close()

	cache := newAdapterProbeCache(time.Minute)
	item := AdapterDescriptor{ID: "grok2api", LocalURL: server.URL + "/v1"}
	before := cache.probe(context.Background(), item)
	if before.ready || !before.authRequired {
		t.Fatalf("expired credential classified as %+v", before)
	}
	t.Setenv("GROK2API_KEY", rotatedKey)
	after := cache.probe(context.Background(), item)
	if !after.ready || after.authRequired || after.modelCount != 1 {
		t.Fatalf("rotated credential reused stale cache result: %+v", after)
	}
	if got := modelCalls.Load(); got != 2 {
		t.Fatalf("model probes=%d, want one probe per credential generation", got)
	}
}

func TestAdapterProbeAllUsesBoundedParallelism(t *testing.T) {
	var active, maximum, calls atomic.Int64
	reachedLimit := make(chan struct{})
	exceededLimit := make(chan struct{})
	release := make(chan struct{})
	var reachedOnce, exceededOnce, releaseOnce sync.Once
	releaseProbes := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		calls.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		if current == adapterProbeParallelism {
			reachedOnce.Do(func() { close(reachedLimit) })
		}
		if current > adapterProbeParallelism {
			exceededOnce.Do(func() { close(exceededLimit) })
		}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer releaseProbes()

	items := make([]AdapterDescriptor, 20)
	for index := range items {
		items[index] = AdapterDescriptor{ID: string(rune('a' + index)), LocalURL: server.URL + "/v1", InstallStatus: "installed"}
	}
	cache := newAdapterProbeCache(time.Minute)
	cache.client = &http.Client{Timeout: 2 * time.Second}
	var probes sync.WaitGroup
	probes.Add(2)
	for _, batch := range [][]AdapterDescriptor{items[:10], items[10:]} {
		go func() {
			defer probes.Done()
			cache.probeAll(context.Background(), batch)
		}()
	}
	select {
	case <-reachedLimit:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe workers did not reach expected parallelism")
	}
	select {
	case <-exceededLimit:
		t.Fatalf("parallel probes exceeded global limit %d", adapterProbeParallelism)
	case <-time.After(100 * time.Millisecond):
	}
	releaseProbes()
	probes.Wait()
	if got := maximum.Load(); got > adapterProbeParallelism {
		t.Fatalf("maximum parallel probes=%d, limit=%d", got, adapterProbeParallelism)
	}
	if got := calls.Load(); got != int64(len(items)) {
		t.Fatalf("network probes=%d, want %d", got, len(items))
	}
}
