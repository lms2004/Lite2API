package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"strconv"
	"strings"
)

type contentSummary struct {
	Text  int
	Image int
	Audio int
	Video int
	File  int
}

func (s contentSummary) Total() int {
	return s.Text + s.Image + s.Audio + s.Video + s.File
}

func (s contentSummary) Kind() string {
	parts := make([]string, 0, 5)
	if s.Text > 0 {
		parts = append(parts, "text")
	}
	if s.Image > 0 {
		parts = append(parts, "image")
	}
	if s.Audio > 0 {
		parts = append(parts, "audio")
	}
	if s.Video > 0 {
		parts = append(parts, "video")
	}
	if s.File > 0 {
		parts = append(parts, "file")
	}
	return strings.Join(parts, "+")
}

func (s *contentSummary) addType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "image"):
		s.Image++
	case strings.Contains(value, "audio"):
		s.Audio++
	case strings.Contains(value, "video"):
		s.Video++
	case strings.Contains(value, "file"):
		s.File++
	case strings.Contains(value, "text"), value == "prompt", value == "message":
		s.Text++
	default:
		return false
	}
	return true
}

func inspectRequest(envelope map[string]json.RawMessage, operation string) contentSummary {
	var summary contentSummary
	keys := []string{"messages", "input", "prompt", "contents", "query", "documents", "image", "images"}
	if operation == "openai.images" {
		keys = []string{"prompt", "image", "images"}
	}
	for _, key := range keys {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			inspectInputValue(&summary, value, key)
		}
	}
	if summary.Total() == 0 {
		switch operation {
		case "openai.chat", "openai.responses", "anthropic.messages", "openai.embeddings", "openai.images", "openai.rerank":
			summary.Text = 1
		}
	}
	return summary
}

func inspectInputValue(summary *contentSummary, value any, keyHint string) {
	switch typed := value.(type) {
	case string:
		if keyHint != "" {
			summary.Text++
		}
	case []any:
		for _, item := range typed {
			inspectInputValue(summary, item, keyHint)
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && summary.addType(kind) {
			return
		}
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "content", "contents", "messages", "input", "prompt", "text", "parts", "items", "query", "documents":
				inspectInputValue(summary, child, key)
			case "image", "images", "image_url", "input_image":
				summary.Image++
			case "audio", "input_audio":
				summary.Audio++
			case "video", "input_video":
				summary.Video++
			case "file", "files", "input_file":
				summary.File++
			case "source":
				if source, ok := child.(map[string]any); ok {
					if sourceType, ok := source["type"].(string); ok && strings.Contains(strings.ToLower(sourceType), "image") {
						summary.Image++
					}
				}
			}
		}
	}
}

type responseCapture struct {
	body        io.ReadCloser
	contentType string
	bytes       int64
	head        []byte
	tail        tailBytes
}

const (
	responseHeadLimit = 8 << 10
	responseTailLimit = 48 << 10
)

func newResponseCapture(body io.ReadCloser, contentType string) *responseCapture {
	return &responseCapture{body: body, contentType: contentType, tail: tailBytes{limit: responseTailLimit}}
}

func (c *responseCapture) Read(data []byte) (int, error) {
	n, err := c.body.Read(data)
	if n > 0 {
		c.bytes += int64(n)
		if len(c.head) < responseHeadLimit {
			remaining := responseHeadLimit - len(c.head)
			if remaining > n {
				remaining = n
			}
			c.head = append(c.head, data[:remaining]...)
		}
		c.tail.Write(data[:n])
	}
	return n, err
}

func (c *responseCapture) Close() error { return c.body.Close() }

func (c *responseCapture) payloads() [][]byte {
	if c.bytes <= int64(len(c.head)) {
		return [][]byte{c.head}
	}
	return [][]byte{c.head, c.tail.Bytes()}
}

type tailBytes struct {
	data  []byte
	limit int
}

func (b *tailBytes) Write(data []byte) {
	if b.limit <= 0 || len(data) == 0 {
		return
	}
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return
	}
	if overflow := len(b.data) + len(data) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
}

func (b *tailBytes) Bytes() []byte { return append([]byte(nil), b.data...) }

type tokenUsage struct {
	Input, Output, Total, Cached, CacheWrite int64
	InputKnown, OutputKnown, TotalKnown      bool
	CachedKnown, CacheWriteKnown             bool
}

func (u tokenUsage) Available() bool {
	return u.InputKnown || u.OutputKnown || u.TotalKnown || u.CachedKnown || u.CacheWriteKnown
}

func parseResponseMetadata(contentType string, payloads [][]byte) (tokenUsage, contentSummary) {
	var usage tokenUsage
	var output contentSummary
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "image/") {
		output.Image++
	}
	for _, payload := range payloads {
		if len(payload) == 0 {
			continue
		}
		if strings.Contains(strings.ToLower(contentType), "event-stream") {
			for _, line := range bytes.Split(payload, []byte{'\n'}) {
				line = bytes.TrimSpace(line)
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if bytes.Equal(line, []byte("[DONE]")) {
					continue
				}
				parseResponseJSON(line, &usage, &output)
			}
			continue
		}
		parseResponseJSON(payload, &usage, &output)
	}
	return usage, output
}

func parseResponseJSON(payload []byte, usage *tokenUsage, output *contentSummary) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		walkResponseValue(value, usage)
		inspectResponseValue(output, value, "")
		return
	}
	// A large JSON response may leave only its tail in the bounded capture.
	// Decode a complete usage object if it is still present in that tail.
	lower := bytes.ToLower(payload)
	if index := bytes.Index(lower, []byte(`"usage"`)); index >= 0 {
		colon := bytes.IndexByte(payload[index+len(`"usage"`):], ':')
		if colon >= 0 {
			start := index + len(`"usage"`) + colon + 1
			var fragment map[string]any
			if json.NewDecoder(bytes.NewReader(bytes.TrimSpace(payload[start:]))).Decode(&fragment) == nil {
				mergeUsage(usage, usageFromMap(fragment))
			}
		}
	}
	if bytes.Contains(lower, []byte(`"b64_json"`)) || bytes.Contains(lower, []byte(`"image_url"`)) {
		output.Image++
	}
}

func walkResponseValue(value any, usage *tokenUsage) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkResponseValue(item, usage)
		}
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "usage") {
				if object, ok := child.(map[string]any); ok {
					mergeUsage(usage, usageFromMap(object))
				}
			}
			walkResponseValue(child, usage)
		}
	}
}

func inspectResponseValue(summary *contentSummary, value any, keyHint string) {
	switch typed := value.(type) {
	case string:
		if keyHint == "content" || keyHint == "text" || keyHint == "output_text" || keyHint == "delta" {
			summary.Text++
		}
	case []any:
		for _, item := range typed {
			inspectResponseValue(summary, item, keyHint)
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && summary.addType(kind) {
			return
		}
		if _, ok := typed["b64_json"]; ok {
			summary.Image++
		}
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "content", "choices", "message", "delta", "output", "outputs", "data", "parts", "items":
				inspectResponseValue(summary, child, strings.ToLower(key))
			case "text", "output_text":
				if _, ok := child.(string); ok {
					summary.Text++
				}
			case "image_url", "output_image", "image":
				summary.Image++
			case "audio", "output_audio":
				summary.Audio++
			case "video", "output_video":
				summary.Video++
			}
		}
	}
}

func usageFromMap(values map[string]any) tokenUsage {
	var usage tokenUsage
	usage.Input, usage.InputKnown = firstNumber(values, "prompt_tokens", "input_tokens", "prompt_token_count", "input_token_count")
	usage.Output, usage.OutputKnown = firstNumber(values, "completion_tokens", "output_tokens", "candidates_token_count", "output_token_count")
	usage.Total, usage.TotalKnown = firstNumber(values, "total_tokens", "total_token_count")
	usage.Cached, usage.CachedKnown = firstNumber(values, "cached_tokens", "cache_read_input_tokens", "cache_read_tokens", "cached_content_token_count")
	usage.CacheWrite, usage.CacheWriteKnown = firstNumber(values, "cache_creation_input_tokens", "cache_creation_tokens", "cache_write_input_tokens")
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details", "cache_details"} {
		if details, ok := values[key].(map[string]any); ok {
			if value, known := firstNumber(details, "cached_tokens", "cache_read_input_tokens", "cached_content_token_count"); known {
				usage.Cached, usage.CachedKnown = value, true
			}
			if value, known := firstNumber(details, "cache_creation_input_tokens", "cache_write_input_tokens"); known {
				usage.CacheWrite, usage.CacheWriteKnown = value, true
			}
		}
	}
	if !usage.TotalKnown && usage.InputKnown && usage.OutputKnown {
		usage.Total, usage.TotalKnown = usage.Input+usage.Output, true
	}
	if !usage.InputKnown && usage.TotalKnown && usage.OutputKnown && usage.Total >= usage.Output {
		usage.Input, usage.InputKnown = usage.Total-usage.Output, true
	}
	return usage
}

func firstNumber(values map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			if parsed, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
				return parsed, true
			}
		case float64:
			return int64(typed), true
		case int64:
			return typed, true
		}
	}
	return 0, false
}

func mergeUsage(target *tokenUsage, source tokenUsage) {
	if source.InputKnown {
		target.Input, target.InputKnown = source.Input, true
	}
	if source.OutputKnown {
		target.Output, target.OutputKnown = source.Output, true
	}
	if source.TotalKnown {
		target.Total, target.TotalKnown = source.Total, true
	}
	if source.CachedKnown {
		target.Cached, target.CachedKnown = source.Cached, true
	}
	if source.CacheWriteKnown {
		target.CacheWrite, target.CacheWriteKnown = source.CacheWrite, true
	}
}

func applyUsage(record *RequestRecord, usage tokenUsage) {
	if !usage.Available() {
		return
	}
	record.UsageAvailable = true
	if usage.InputKnown {
		record.InputTokens = usage.Input
	}
	if usage.OutputKnown {
		record.OutputTokens = usage.Output
	}
	if usage.TotalKnown {
		record.TotalTokens = usage.Total
	}
	if usage.CachedKnown {
		record.CachedTokens = usage.Cached
	}
	if usage.CacheWriteKnown {
		record.CacheWriteTokens = usage.CacheWrite
	}
	if usage.InputKnown && usage.CachedKnown && usage.Input > 0 {
		record.CacheRate = float64(usage.Cached) * 100 / float64(usage.Input)
		if record.CacheRate > 100 {
			record.CacheRate = 100
		}
		record.CacheRateKnown = true
	}
}

func applyBufferedResponseMetadata(record *RequestRecord, headers map[string][]string, body []byte) {
	contentType := ""
	for key, values := range headers {
		if strings.EqualFold(key, "Content-Type") && len(values) > 0 {
			contentType = values[0]
			break
		}
	}
	usage, output := parseResponseMetadata(contentType, [][]byte{body})
	record.ResponseBytes = int64(len(body))
	applyUsage(record, usage)
	if kind := output.Kind(); kind != "" {
		record.OutputType = kind
	}
}

func applyCapturedResponseMetadata(record *RequestRecord, capture *responseCapture) {
	usage, output := parseResponseMetadata(capture.contentType, capture.payloads())
	record.ResponseBytes = capture.bytes
	applyUsage(record, usage)
	if kind := output.Kind(); kind != "" {
		record.OutputType = kind
	}
}
