package gateway

import (
	"bufio"
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

const requestLogQueueSize = 256

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
	path    string
	maxSize int64
	backups int

	mu     sync.RWMutex
	queue  chan RequestRecord
	closed bool
	done   chan struct{}

	file         *os.File
	currentBytes atomic.Int64
	dropped      atomic.Int64
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
		path:    filepath.Clean(path),
		maxSize: maxSize,
		backups: backups,
		queue:   make(chan RequestRecord, requestLogQueueSize),
		done:    make(chan struct{}),
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
	for record := range l.queue {
		if err := l.write(record); err != nil {
			slog.Error("request log write failed", "error", err)
		}
	}
	if l.file != nil {
		_ = l.file.Sync()
		_ = l.file.Close()
		l.file = nil
	}
}

func (l *requestLogWriter) write(record RequestRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if l.currentBytes.Load() > 0 && l.currentBytes.Load()+int64(len(data)) > l.maxSize {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	if len(data) > int(l.maxSize) {
		// A request record is bounded by the fields captured in memory. Keep the
		// newest record rather than allowing one unusually long error to break
		// the retention limit.
		data = append(data[:0], data[len(data)-int(l.maxSize):]...)
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

// loadRequestRecords restores valid records from the persistent request log
// and its rotation backups. The records are sorted so Stats can rebuild both
// the recent list and the minute trend in chronological order.
func loadRequestRecords(path string, backups int) ([]RequestRecord, error) {
	records := make([]RequestRecord, 0)
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
			return nil, fmt.Errorf("open request log backup: %w", err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		for scanner.Scan() {
			var record RequestRecord
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, record.Time); err != nil {
				continue
			}
			records = append(records, record)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("read request log backup: %w", scanErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close request log backup: %w", closeErr)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, records[i].Time)
		right, _ := time.Parse(time.RFC3339Nano, records[j].Time)
		return left.Before(right)
	})
	return records, nil
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
