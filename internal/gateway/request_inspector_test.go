package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestInspectRequestDetectsTextAndImage(t *testing.T) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"describe\"},{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64,abc\"}}]}]}"), &envelope); err != nil {
		t.Fatal(err)
	}
	got := inspectRequest(envelope, "openai.chat")
	if got.Kind() != "text+image" || got.Text != 1 || got.Image != 1 {
		t.Fatalf("summary=%+v kind=%q", got, got.Kind())
	}
}

func TestParseResponseUsageAndCacheRate(t *testing.T) {
	record := RequestRecord{}
	body := []byte("{\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120,\"prompt_tokens_details\":{\"cached_tokens\":25}},\"choices\":[{\"message\":{\"content\":\"done\"}}]}")
	applyBufferedResponseMetadata(&record, map[string][]string{"Content-Type": {"application/json"}}, body)
	if !record.UsageAvailable || record.InputTokens != 100 || record.OutputTokens != 20 || record.TotalTokens != 120 {
		t.Fatalf("usage=%+v", record)
	}
	if !record.CacheRateKnown || record.CachedTokens != 25 || record.CacheRate != 25 {
		t.Fatalf("cache=%+v", record)
	}
	if record.OutputType != "text" {
		t.Fatalf("output_type=%q", record.OutputType)
	}
}

func TestParseStreamingUsage(t *testing.T) {
	record := RequestRecord{}
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}\n\ndata: [DONE]\n"
	capture := newResponseCapture(ioNopReadCloser{strings.NewReader(payload)}, "text/event-stream")
	buf := make([]byte, 128)
	for {
		_, err := capture.Read(buf)
		if err != nil {
			break
		}
	}
	applyCapturedResponseMetadata(&record, capture)
	if !record.UsageAvailable || record.InputTokens != 7 || record.OutputTokens != 3 || record.OutputType != "text" {
		t.Fatalf("stream record=%+v", record)
	}
}

func TestRequestLogRotatesWithinBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.log")
	logger, err := newRequestLogWriter(path, 64<<10, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		for len(logger.queue) == cap(logger.queue) {
			time.Sleep(time.Millisecond)
		}
		logger.Enqueue(RequestRecord{RequestID: strings.Repeat("x", 64), Model: "model", Error: strings.Repeat("e", 256)})
	}
	for len(logger.queue) == cap(logger.queue) {
		time.Sleep(time.Millisecond)
	}
	logger.Enqueue(RequestRecord{RequestID: "oversized", Model: strings.Repeat("m", 1<<20), Error: strings.Repeat("e", 1<<20)})
	logger.Close()
	for _, name := range []string{"request.log", "request.log.1"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Size() > 64<<10 {
			t.Fatalf("%s size=%d exceeds max", name, info.Size())
		}
		contents, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
			if line != "" && !json.Valid([]byte(line)) {
				t.Fatalf("%s contains a corrupt JSON line: %.80q", name, line)
			}
		}
	}
}

func TestLoadLatestRequestRecordAcrossBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.log")
	older := RequestRecord{Time: "2026-08-15T10:00:00Z", RequestID: "older", Status: 200}
	newer := RequestRecord{Time: "2026-08-17T10:00:00Z", RequestID: "newer", Status: 503}
	olderJSON, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	newerJSON, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte("not-json\n"), olderJSON...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", append(newerJSON, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadLatestRequestRecord(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RequestID != newer.RequestID {
		t.Fatalf("latest=%+v want %q", got, newer.RequestID)
	}
}

func TestRequestLogRecoveryKeepsBoundedNewestSetAndPerRouteLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.log")
	fingerprints := map[string]string{"hot": "hot-fingerprint", "cold": "cold-fingerprint"}
	base := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	var contents strings.Builder
	for index := 0; index < 100; index++ {
		model := "hot"
		if index == 3 {
			model = "cold"
		}
		record := RequestRecord{
			Time:      base.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano),
			RequestID: fmt.Sprintf("request-%03d", index), Model: model, AccountID: "upstream",
			Status: http.StatusOK, Outcome: "success", RouteFingerprint: fingerprints[model],
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(encoded)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0600); err != nil {
		t.Fatal(err)
	}
	records, routeLatest, err := loadRequestState(path, 0, 10, fingerprints)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 10 || records[0].RequestID != "request-090" || records[9].RequestID != "request-099" {
		t.Fatalf("bounded recovery returned wrong newest set: len=%d first=%+v last=%+v", len(records), records[0], records[len(records)-1])
	}
	if cold := routeLatest[routeObservationKey("cold", "")]; cold.RequestID != "request-003" {
		t.Fatalf("low-volume route latest observation was lost: %+v", cold)
	}
}

func TestGatewayRecoversFullCurrentLogBeforeZeroBackupRotation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	logPath := filepath.Join(directory, "request.log")
	cfg := config.Defaults()
	cfg.Server.APIKeys = []string{"gateway-secret"}
	cfg.Server.AdminToken = "admin-secret"
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Server.RequestLogPath = "request.log"
	cfg.Server.RequestLogMaxBytes = 64 << 10
	cfg.Server.RequestLogBackups = 0
	cfg.Accounts = []config.Account{{
		ID: "main", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test",
		Models: []string{"real"}, Enabled: true, Weight: 1,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}}
	cfg.Routes = map[string]config.Route{
		"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "main"}}},
	}
	if err := config.NewStore(configPath).Save(cfg); err != nil {
		t.Fatal(err)
	}
	normalized := config.Normalize(cfg)
	record := RequestRecord{
		Time: time.Now().UTC().Format(time.RFC3339Nano), RequestID: "before-startup-rotation",
		Model: "alias", AccountID: "main", Status: http.StatusOK, Outcome: "success",
		RouteFingerprint: buildRouteFingerprints(normalized)["alias"],
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	contents := append(append(line, '\n'), bytes.Repeat([]byte("x"), int(cfg.Server.RequestLogMaxBytes)-len(line)-1)...)
	if err := os.WriteFile(logPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	g, err := New(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	latest := g.Stats().RouteLatest
	if len(latest) != 1 || latest[0].RequestID != record.RequestID {
		t.Fatalf("startup rotation destroyed readiness recovery: %+v", latest)
	}
}

type ioNopReadCloser struct{ *strings.Reader }

func (ioNopReadCloser) Close() error { return nil }
