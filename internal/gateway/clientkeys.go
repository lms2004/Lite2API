package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	managedKeyPrefix = "sk-l2a-"
	rateCountBits    = 20
	rateCountMask    = (1 << rateCountBits) - 1
)

type ClientKeyRecord struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	SecretHash  string   `json:"secret_hash"`
	Enabled     bool     `json:"enabled"`
	Models      []string `json:"models,omitempty"`
	RPM         int      `json:"rpm,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

type ClientKeyCreate struct {
	Name        string   `json:"name"`
	Models      []string `json:"models,omitempty"`
	RPM         int      `json:"rpm,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

type ClientKeyUpdate struct {
	Name        *string   `json:"name,omitempty"`
	Models      *[]string `json:"models,omitempty"`
	RPM         *int      `json:"rpm,omitempty"`
	Concurrency *int      `json:"concurrency,omitempty"`
	ExpiresAt   *string   `json:"expires_at,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
}

type ClientKeySnapshot struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Prefix        string   `json:"prefix"`
	Enabled       bool     `json:"enabled"`
	Models        []string `json:"models,omitempty"`
	RPM           int      `json:"rpm"`
	Concurrency   int      `json:"concurrency"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	CreatedAt     string   `json:"created_at"`
	LastUsedAt    string   `json:"last_used_at,omitempty"`
	TotalRequests int64    `json:"total_requests"`
	Successful    int64    `json:"successful_requests"`
	Active        int64    `json:"active"`
}

type clientKeyMeta struct {
	record      ClientKeyRecord
	hash        [sha256.Size]byte
	models      map[string]struct{}
	expiresUnix int64
}

type clientKeyRuntime struct {
	meta     atomic.Pointer[clientKeyMeta]
	active   atomic.Int64
	total    atomic.Int64
	success  atomic.Int64
	lastUsed atomic.Int64
	rate     atomic.Uint64
}

type clientKeySnapshot map[string]*clientKeyRuntime

type ClientKeyStore struct {
	path     string
	writeMu  sync.Mutex
	snapshot atomic.Pointer[clientKeySnapshot]
}

type KeyLease struct {
	ID      string
	Name    string
	models  map[string]struct{}
	runtime *clientKeyRuntime
	done    atomic.Bool
}

type KeyAuthFailure string

const (
	KeyAuthInvalid     KeyAuthFailure = "invalid"
	KeyAuthRateLimited KeyAuthFailure = "rate_limited"
	KeyAuthConcurrency KeyAuthFailure = "concurrency_limited"
)

type clientKeyFile struct {
	Version int               `json:"version"`
	Keys    []ClientKeyRecord `json:"keys"`
}

func NewClientKeyStore(path string) (*ClientKeyStore, error) {
	s := &ClientKeyStore{path: path}
	empty := clientKeySnapshot{}
	s.snapshot.Store(&empty)
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func ResolveClientKeysPath(configPath, configured string) string {
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(filepath.Dir(configPath), configured)
}

func (s *ClientKeyStore) Path() string { return s.path }

func (s *ClientKeyStore) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read client keys: %w", err)
	}
	var file clientKeyFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("parse client keys: %w", err)
	}
	if file.Version != 1 {
		return fmt.Errorf("unsupported client key file version %d", file.Version)
	}
	next := make(clientKeySnapshot, len(file.Keys))
	for _, record := range file.Keys {
		meta, err := buildClientKeyMeta(record)
		if err != nil {
			return fmt.Errorf("client key %q: %w", record.ID, err)
		}
		if _, exists := next[record.ID]; exists {
			return fmt.Errorf("duplicate client key id %q", record.ID)
		}
		runtime := &clientKeyRuntime{}
		runtime.meta.Store(meta)
		next[record.ID] = runtime
	}
	s.snapshot.Store(&next)
	return nil
}

func (s *ClientKeyStore) Authenticate(raw string, legacy map[[sha256.Size]byte]struct{}) (*KeyLease, KeyAuthFailure) {
	if raw == "" || len(raw) > 512 {
		return nil, KeyAuthInvalid
	}
	if id, ok := managedKeyID(raw); ok {
		snapshot := s.snapshot.Load()
		runtime := (*snapshot)[id]
		if runtime == nil {
			return nil, KeyAuthInvalid
		}
		meta := runtime.meta.Load()
		candidate := sha256.Sum256([]byte(raw))
		if subtle.ConstantTimeCompare(candidate[:], meta.hash[:]) != 1 || !meta.record.Enabled {
			return nil, KeyAuthInvalid
		}
		now := time.Now()
		if meta.expiresUnix > 0 && now.Unix() >= meta.expiresUnix {
			return nil, KeyAuthInvalid
		}
		if !acquireRPM(&runtime.rate, meta.record.RPM, now) {
			return nil, KeyAuthRateLimited
		}
		if !acquireAtomic(&runtime.active, int64(meta.record.Concurrency)) {
			return nil, KeyAuthConcurrency
		}
		runtime.total.Add(1)
		runtime.lastUsed.Store(now.Unix())
		return &KeyLease{ID: meta.record.ID, Name: meta.record.Name, models: meta.models, runtime: runtime}, ""
	}
	candidate := sha256.Sum256([]byte(raw))
	if _, ok := legacy[candidate]; !ok {
		return nil, KeyAuthInvalid
	}
	return &KeyLease{ID: "legacy-env", Name: "Legacy environment key"}, ""
}

func (l *KeyLease) AllowsModel(model string) bool {
	if l == nil || len(l.models) == 0 {
		return true
	}
	if _, ok := l.models["*"]; ok {
		return true
	}
	_, ok := l.models[model]
	return ok
}

func (l *KeyLease) Complete(success bool) {
	if l == nil {
		return
	}
	if l.done.CompareAndSwap(false, true) {
		if l.runtime == nil {
			return
		}
		if success {
			l.runtime.success.Add(1)
		}
		l.runtime.active.Add(-1)
	}
}

func acquireAtomic(counter *atomic.Int64, limit int64) bool {
	if limit <= 0 {
		counter.Add(1)
		return true
	}
	for {
		current := counter.Load()
		if current >= limit {
			return false
		}
		if counter.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func acquireRPM(bucket *atomic.Uint64, rpm int, now time.Time) bool {
	if rpm <= 0 {
		return true
	}
	minute := uint64(now.Unix() / 60)
	for {
		current := bucket.Load()
		count := current & rateCountMask
		var next uint64
		if current>>rateCountBits != minute {
			next = minute<<rateCountBits | 1
		} else {
			if count >= uint64(rpm) {
				return false
			}
			next = current + 1
		}
		if bucket.CompareAndSwap(current, next) {
			return true
		}
	}
}

func (s *ClientKeyStore) Create(input ClientKeyCreate) (ClientKeySnapshot, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = "API Key " + time.Now().UTC().Format("20060102-150405")
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	idBytes, secretBytes := make([]byte, 8), make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return ClientKeySnapshot{}, "", err
	}
	if _, err := rand.Read(secretBytes); err != nil {
		return ClientKeySnapshot{}, "", err
	}
	id := hex.EncodeToString(idBytes)
	full := managedKeyPrefix + id + "." + base64.RawURLEncoding.EncodeToString(secretBytes)
	hash := sha256.Sum256([]byte(full))
	record := ClientKeyRecord{
		ID: id, Name: input.Name, Prefix: managedKeyPrefix + id + "...",
		SecretHash: hex.EncodeToString(hash[:]), Enabled: enabled, Models: input.Models,
		RPM: input.RPM, Concurrency: input.Concurrency, ExpiresAt: input.ExpiresAt,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	meta, err := buildClientKeyMeta(record)
	if err != nil {
		return ClientKeySnapshot{}, "", err
	}
	runtime := &clientKeyRuntime{}
	runtime.meta.Store(meta)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	current := s.snapshot.Load()
	next := cloneClientKeySnapshot(*current)
	if _, exists := next[id]; exists {
		return ClientKeySnapshot{}, "", errors.New("generated duplicate key id")
	}
	next[id] = runtime
	if err := s.persist(next); err != nil {
		return ClientKeySnapshot{}, "", err
	}
	s.snapshot.Store(&next)
	return runtime.snapshot(), full, nil
}

func (s *ClientKeyStore) Update(id string, input ClientKeyUpdate) (ClientKeySnapshot, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	current := s.snapshot.Load()
	runtime := (*current)[id]
	if runtime == nil {
		return ClientKeySnapshot{}, errors.New("client key not found")
	}
	record := runtime.meta.Load().record
	if input.Name != nil {
		record.Name = *input.Name
	}
	if input.Models != nil {
		record.Models = *input.Models
	}
	if input.RPM != nil {
		record.RPM = *input.RPM
	}
	if input.Concurrency != nil {
		record.Concurrency = *input.Concurrency
	}
	if input.ExpiresAt != nil {
		record.ExpiresAt = *input.ExpiresAt
	}
	if input.Enabled != nil {
		record.Enabled = *input.Enabled
	}
	meta, err := buildClientKeyMeta(record)
	if err != nil {
		return ClientKeySnapshot{}, err
	}
	next := cloneClientKeySnapshot(*current)
	if err := s.persistWithReplacement(next, id, meta); err != nil {
		return ClientKeySnapshot{}, err
	}
	runtime.meta.Store(meta)
	s.snapshot.Store(&next)
	return runtime.snapshot(), nil
}

func (s *ClientKeyStore) persistWithReplacement(next clientKeySnapshot, id string, meta *clientKeyMeta) error {
	old := next[id]
	replacement := &clientKeyRuntime{}
	replacement.meta.Store(meta)
	next[id] = replacement
	err := s.persist(next)
	next[id] = old
	return err
}

func (s *ClientKeyStore) Delete(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	current := s.snapshot.Load()
	if (*current)[id] == nil {
		return errors.New("client key not found")
	}
	next := cloneClientKeySnapshot(*current)
	delete(next, id)
	if err := s.persist(next); err != nil {
		return err
	}
	s.snapshot.Store(&next)
	return nil
}

func (s *ClientKeyStore) List() []ClientKeySnapshot {
	current := s.snapshot.Load()
	result := make([]ClientKeySnapshot, 0, len(*current))
	for _, runtime := range *current {
		result = append(result, runtime.snapshot())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result
}

func (r *clientKeyRuntime) snapshot() ClientKeySnapshot {
	meta := r.meta.Load()
	lastUsed := ""
	if unix := r.lastUsed.Load(); unix > 0 {
		lastUsed = time.Unix(unix, 0).UTC().Format(time.RFC3339)
	}
	return ClientKeySnapshot{
		ID: meta.record.ID, Name: meta.record.Name, Prefix: meta.record.Prefix,
		Enabled: meta.record.Enabled, Models: append([]string(nil), meta.record.Models...),
		RPM: meta.record.RPM, Concurrency: meta.record.Concurrency,
		ExpiresAt: meta.record.ExpiresAt, CreatedAt: meta.record.CreatedAt,
		LastUsedAt: lastUsed, TotalRequests: r.total.Load(), Successful: r.success.Load(),
		Active: r.active.Load(),
	}
}

func buildClientKeyMeta(record ClientKeyRecord) (*clientKeyMeta, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.Name = strings.TrimSpace(record.Name)
	if len(record.ID) != 16 {
		return nil, errors.New("id must be 16 hexadecimal characters")
	}
	if _, err := hex.DecodeString(record.ID); err != nil {
		return nil, errors.New("id must be hexadecimal")
	}
	if record.Name == "" || len(record.Name) > 80 {
		return nil, errors.New("name must contain 1 to 80 characters")
	}
	if record.Prefix != managedKeyPrefix+record.ID+"..." {
		return nil, errors.New("invalid key prefix")
	}
	hashBytes, err := hex.DecodeString(record.SecretHash)
	if err != nil || len(hashBytes) != sha256.Size {
		return nil, errors.New("invalid secret hash")
	}
	if record.RPM < 0 || record.RPM > rateCountMask {
		return nil, fmt.Errorf("rpm must be between 0 and %d", rateCountMask)
	}
	if record.Concurrency < 0 || record.Concurrency > 10000 {
		return nil, errors.New("concurrency must be between 0 and 10000")
	}
	created, err := time.Parse(time.RFC3339, record.CreatedAt)
	if err != nil || created.IsZero() {
		return nil, errors.New("invalid created_at")
	}
	expiresUnix := int64(0)
	if record.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, record.ExpiresAt)
		if err != nil {
			return nil, errors.New("expires_at must be RFC3339")
		}
		expiresUnix = expires.Unix()
	}
	models := make(map[string]struct{}, len(record.Models))
	cleanModels := make([]string, 0, len(record.Models))
	if len(record.Models) > 256 {
		return nil, errors.New("models cannot contain more than 256 entries")
	}
	for _, model := range record.Models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 128 {
			return nil, errors.New("model names must contain 1 to 128 characters")
		}
		if _, exists := models[model]; exists {
			continue
		}
		models[model] = struct{}{}
		cleanModels = append(cleanModels, model)
	}
	record.Models = cleanModels
	var hash [sha256.Size]byte
	copy(hash[:], hashBytes)
	return &clientKeyMeta{record: record, hash: hash, models: models, expiresUnix: expiresUnix}, nil
}

func managedKeyID(raw string) (string, bool) {
	if !strings.HasPrefix(raw, managedKeyPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(raw, managedKeyPrefix)
	dot := strings.IndexByte(rest, '.')
	if dot != 16 || len(rest) < dot+32 {
		return "", false
	}
	id := rest[:dot]
	for i := 0; i < len(id); i++ {
		if !isLowerHex(id[i]) {
			return "", false
		}
	}
	return id, true
}

func isLowerHex(value byte) bool { return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' }

func cloneClientKeySnapshot(source clientKeySnapshot) clientKeySnapshot {
	result := make(clientKeySnapshot, len(source))
	for id, runtime := range source {
		result[id] = runtime
	}
	return result
}

func (s *ClientKeyStore) persist(snapshot clientKeySnapshot) error {
	records := make([]ClientKeyRecord, 0, len(snapshot))
	for _, runtime := range snapshot {
		records = append(records, runtime.meta.Load().record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	data, err := json.MarshalIndent(clientKeyFile{Version: 1, Keys: records}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".client-keys-*.json")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
