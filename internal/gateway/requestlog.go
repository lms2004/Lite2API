package gateway

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	requestLogQueueSize        = 256
	maxRecoveredRequestRecords = 16_384
)

// RequestLogStatus is intentionally small: it lets the admin page explain the
// retention boundary without exposing a server filesystem path.
type RequestLogStatus struct {
	Enabled        bool  `json:"enabled"`
	CurrentBytes   int64 `json:"current_bytes"`
	MaxBytes       int64 `json:"max_bytes"`
	Backups        int   `json:"backups"`
	RetentionBytes int64 `json:"retention_bytes"`
	Queued         int   `json:"queued"`
	Dropped        int64 `json:"dropped"`
}

type requestLogWriter struct {
	path     string
	maxSize  int64
	backups  int
	configMu sync.RWMutex

	mu       sync.RWMutex
	queue    chan RequestRecord
	commands chan requestLogReconfigure
	closed   bool
	done     chan struct{}

	file         *os.File
	currentBytes atomic.Int64
	dropped      atomic.Int64
}

func (l *requestLogWriter) matches(path string, maxSize int64, backups int) bool {
	if l == nil {
		return false
	}
	l.configMu.RLock()
	matches := l.path == filepath.Clean(path) && l.maxSize == maxSize && l.backups == backups
	l.configMu.RUnlock()
	return matches
}

func (l *requestLogWriter) configuration() (string, int64, int) {
	if l == nil {
		return "", 0, 0
	}
	l.configMu.RLock()
	path, maxSize, backups := l.path, l.maxSize, l.backups
	l.configMu.RUnlock()
	return path, maxSize, backups
}

type requestLogReconfigure struct {
	path     string
	maxSize  int64
	backups  int
	complete chan error
}

func newRequestLogWriter(path string, maxSize int64, backups int) (*requestLogWriter, error) {
	if path == "" {
		return nil, fmt.Errorf("request log path is empty")
	}
	if maxSize < 64<<10 {
		return nil, fmt.Errorf("request log max size must be at least 64KiB")
	}
	if backups < 0 {
		return nil, fmt.Errorf("request log backups cannot be negative")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create request log directory: %w", err)
	}
	writer := &requestLogWriter{
		path:     filepath.Clean(path),
		maxSize:  maxSize,
		backups:  backups,
		queue:    make(chan RequestRecord, requestLogQueueSize),
		commands: make(chan requestLogReconfigure),
		done:     make(chan struct{}),
	}
	if err := writer.open(); err != nil {
		return nil, err
	}
	go writer.run()
	return writer, nil
}

func (l *requestLogWriter) open() error {
	info, err := os.Stat(l.path)
	if err == nil && info.Size() >= l.maxSize {
		if err := l.rotate(); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat request log: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open request log: %w", err)
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat open request log: %w", err)
	}
	l.file = file
	l.currentBytes.Store(info.Size())
	return nil
}

func (l *requestLogWriter) run() {
	defer close(l.done)
	for {
		select {
		case record, ok := <-l.queue:
			if !ok {
				if l.file != nil {
					_ = l.file.Sync()
					_ = l.file.Close()
					l.file = nil
				}
				return
			}
			if err := l.write(record); err != nil {
				slog.Error("request log write failed", "error", err)
			}
		case command := <-l.commands:
			command.complete <- l.applyReconfigure(command.path, command.maxSize, command.backups)
		}
	}
}

func (l *requestLogWriter) Reconfigure(path string, maxSize int64, backups int) error {
	if path == "" {
		return fmt.Errorf("request log path is empty")
	}
	if maxSize < 64<<10 {
		return fmt.Errorf("request log max size must be at least 64KiB")
	}
	if backups < 0 {
		return fmt.Errorf("request log backups cannot be negative")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return fmt.Errorf("request log is closed")
	}
	command := requestLogReconfigure{
		path: filepath.Clean(path), maxSize: maxSize, backups: backups, complete: make(chan error, 1),
	}
	l.commands <- command
	return <-command.complete
}

func (l *requestLogWriter) applyReconfigure(path string, maxSize int64, backups int) error {
	l.configMu.Lock()
	defer l.configMu.Unlock()
	oldPath, oldMaxSize, oldBackups := l.path, l.maxSize, l.backups
	if l.file != nil {
		_ = l.file.Sync()
		if err := l.file.Close(); err != nil {
			return err
		}
		l.file = nil
	}
	l.path, l.maxSize, l.backups = path, maxSize, backups
	configureErr := os.MkdirAll(filepath.Dir(path), 0700)
	if configureErr == nil {
		configureErr = l.open()
	}
	if configureErr == nil {
		return nil
	}
	l.path, l.maxSize, l.backups = oldPath, oldMaxSize, oldBackups
	if restoreErr := l.open(); restoreErr != nil {
		return fmt.Errorf("reconfigure request log: %w; restore previous writer: %v", configureErr, restoreErr)
	}
	return fmt.Errorf("reconfigure request log: %w", configureErr)
}

func (l *requestLogWriter) write(record RequestRecord) error {
	record = sanitizeRequestRecord(record)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if int64(len(data)) > l.maxSize {
		// Preserve newline-delimited JSON even if a future record field escapes
		// the normal observation budget. Byte-slicing JSON would corrupt both
		// the current line and all recovery scanners that consume it.
		data, err = json.Marshal(RequestRecord{
			Time: record.Time, RequestID: record.RequestID, Status: record.Status,
			Error: "request log record exceeded the configured record budget",
		})
		if err != nil {
			return err
		}
		data = append(data, '\n')
	}
	if l.currentBytes.Load() > 0 && l.currentBytes.Load()+int64(len(data)) > l.maxSize {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.file.Write(data)
	if err != nil {
		return err
	}
	l.currentBytes.Add(int64(n))
	return nil
}

func (l *requestLogWriter) rotate() error {
	if l.file != nil {
		_ = l.file.Sync()
		if err := l.file.Close(); err != nil {
			return err
		}
		l.file = nil
	}
	if l.backups > 0 {
		for index := l.backups; index >= 1; index-- {
			source := l.path
			if index > 1 {
				source += "." + strconv.Itoa(index-1)
			}
			target := l.path + "." + strconv.Itoa(index)
			if _, err := os.Stat(source); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Rename(source, target); err != nil {
				return err
			}
		}
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open rotated request log: %w", err)
	}
	l.file = file
	l.currentBytes.Store(0)
	return nil
}

func (l *requestLogWriter) Enqueue(record RequestRecord) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return
	}
	select {
	case l.queue <- record:
	default:
		l.dropped.Add(1)
	}
}

func (l *requestLogWriter) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.queue)
	l.mu.Unlock()
	<-l.done
}

func (l *requestLogWriter) Status() RequestLogStatus {
	l.configMu.RLock()
	defer l.configMu.RUnlock()
	return RequestLogStatus{
		Enabled:        true,
		CurrentBytes:   l.currentBytes.Load(),
		MaxBytes:       l.maxSize,
		Backups:        l.backups,
		RetentionBytes: l.maxSize * int64(l.backups+1),
		Queued:         len(l.queue),
		Dropped:        l.dropped.Load(),
	}
}

type recoveredRecord struct {
	record   RequestRecord
	observed time.Time
}

type recoveredRecordHeap []recoveredRecord

func (h recoveredRecordHeap) Len() int           { return len(h) }
func (h recoveredRecordHeap) Less(i, j int) bool { return h[i].observed.Before(h[j].observed) }
func (h recoveredRecordHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *recoveredRecordHeap) Push(value any)    { *h = append(*h, value.(recoveredRecord)) }
func (h *recoveredRecordHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// loadRequestState scans retained files once while keeping only a bounded
// newest-record heap plus one latest observation per configured route. Startup
// memory is independent of the configured multi-gigabyte retention envelope.
func loadRequestState(path string, backups, maxRecords int, routeFingerprints map[string]string) ([]RequestRecord, map[string]RequestRecord, error) {
	if maxRecords < 0 {
		maxRecords = 0
	}
	records := make(recoveredRecordHeap, 0, maxRecords)
	heap.Init(&records)
	routeLatest := make(map[string]recoveredRecord, len(routeFingerprints))
	for index := 0; index <= backups; index++ {
		candidate := path
		if index > 0 {
			candidate += "." + strconv.Itoa(index)
		}
		file, err := os.Open(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("open request log backup: %w", err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		for scanner.Scan() {
			var record RequestRecord
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				continue
			}
			observed, err := time.Parse(time.RFC3339Nano, record.Time)
			if err != nil {
				continue
			}
			record = sanitizeRequestRecord(record)
			recovered := recoveredRecord{record: record, observed: observed}
			if maxRecords > 0 {
				if records.Len() < maxRecords {
					heap.Push(&records, recovered)
				} else if observed.After(records[0].observed) {
					heap.Pop(&records)
					heap.Push(&records, recovered)
				}
			}
			if fingerprint := routeFingerprints[record.Model]; fingerprint != "" && record.RouteFingerprint == fingerprint && routeHealthObservation(record) {
				key := routeObservationKey(record.Model, record.Operation)
				current, exists := routeLatest[key]
				if !exists || observed.After(current.observed) {
					routeLatest[key] = recovered
				}
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, nil, fmt.Errorf("read request log backup: %w", scanErr)
		}
		if closeErr != nil {
			return nil, nil, fmt.Errorf("close request log backup: %w", closeErr)
		}
	}
	result := make([]RequestRecord, len(records))
	for index := range records {
		result[index] = records[index].record
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, result[i].Time)
		right, _ := time.Parse(time.RFC3339Nano, result[j].Time)
		return left.Before(right)
	})
	latest := make(map[string]RequestRecord, len(routeLatest))
	for alias, recovered := range routeLatest {
		latest[alias] = recovered.record
	}
	return result, latest, nil
}

func loadRequestRecords(path string, backups int) ([]RequestRecord, error) {
	records, _, err := loadRequestState(path, backups, maxRecoveredRequestRecords, nil)
	return records, err
}

// loadLatestRequestRecord restores only the newest valid record from the
// persistent request log. Operations health intentionally has a single real
// observation, so a restart must not turn an otherwise verified route into an
// unknown route just because the in-memory ring was recreated.
func loadLatestRequestRecord(path string, backups int) (*RequestRecord, error) {
	records, err := loadRequestRecords(path, backups)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	latest := records[len(records)-1]
	return &latest, nil
}

func resolveRequestLogPath(configPath, configured string) string {
	if configured == "" {
		configured = "request.log"
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(filepath.Dir(configPath), configured)
}
