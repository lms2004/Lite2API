package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	logger.Close()
	for _, name := range []string{"request.log", "request.log.1"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Size() > 64<<10 {
			t.Fatalf("%s size=%d exceeds max", name, info.Size())
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

type ioNopReadCloser struct{ *strings.Reader }

func (ioNopReadCloser) Close() error { return nil }
