#!/usr/bin/env python3
from pathlib import Path

root = Path(__file__).resolve().parents[1]
gateway = root / 'internal/gateway'
admin_path = gateway / 'admin.go'
app_path = root / 'internal/web/app.js'
workflow_path = root / '.github/workflows/apply-quota-history-patch.yml'

go_source = r'''package gateway

import (
    "bufio"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "time"
)

const quotaHistoryRetention = 30 * 24 * time.Hour

type QuotaHistoryObservationInput struct {
    AccountID      string   `json:"account_id"`
    Provider       string   `json:"provider,omitempty"`
    WindowID       string   `json:"window_id"`
    Label          string   `json:"label,omitempty"`
    UsedPercentage *float64 `json:"used_percentage,omitempty"`
    Remaining      *float64 `json:"remaining,omitempty"`
    Unit           string   `json:"unit,omitempty"`
    ResetAt        string   `json:"reset_at,omitempty"`
    ObservedAt     string   `json:"observed_at,omitempty"`
    Source         string   `json:"source,omitempty"`
    Status         string   `json:"status,omitempty"`
}

type QuotaHistoryIngestRequest struct {
    Observations []QuotaHistoryObservationInput `json:"observations"`
}

type QuotaHistoryPoint struct {
    ID        string   `json:"id"`
    Time      string   `json:"time"`
    AccountID string   `json:"account_id"`
    Provider  string   `json:"provider,omitempty"`
    WindowID  string   `json:"window_id"`
    Label     string   `json:"label,omitempty"`
    Used      *float64 `json:"used,omitempty"`
    Remaining *float64 `json:"remaining,omitempty"`
    Unit      string   `json:"unit,omitempty"`
    ResetAt   string   `json:"reset_at,omitempty"`
    ObservedAt string  `json:"observed_at,omitempty"`
    Source    string   `json:"source,omitempty"`
    Status    string   `json:"status,omitempty"`
}

type quotaHistoryStore struct {
    mu     sync.Mutex
    loaded bool
    path   string
    points []QuotaHistoryPoint
}

var adminQuotaHistory = &quotaHistoryStore{}

func quotaHistoryFilePath() string {
    if value := strings.TrimSpace(os.Getenv("LITE2API_QUOTA_HISTORY_PATH")); value != "" {
        return value
    }
    return "quota-history.jsonl"
}

func (s *quotaHistoryStore) ensureLoadedLocked() error {
    if s.loaded {
        return nil
    }
    s.loaded = true
    if s.path == "" {
        s.path = quotaHistoryFilePath()
    }
    file, err := os.Open(s.path)
    if errors.Is(err, os.ErrNotExist) {
        return nil
    }
    if err != nil {
        return err
    }
    defer file.Close()
    cutoff := time.Now().Add(-quotaHistoryRetention)
    scanner := bufio.NewScanner(file)
    scanner.Buffer(make([]byte, 64<<10), 1<<20)
    for scanner.Scan() {
        var point QuotaHistoryPoint
        if json.Unmarshal(scanner.Bytes(), &point) != nil {
            continue
        }
        observed, err := time.Parse(time.RFC3339Nano, point.Time)
        if err != nil || observed.Before(cutoff) {
            continue
        }
        s.points = append(s.points, point)
    }
    sort.Slice(s.points, func(i, j int) bool { return s.points[i].Time < s.points[j].Time })
    return scanner.Err()
}

func (s *quotaHistoryStore) append(request QuotaHistoryIngestRequest) (int, error) {
    if len(request.Observations) == 0 {
        return 0, nil
    }
    if len(request.Observations) > 500 {
        return 0, errors.New("quota observations cannot exceed 500 items")
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    if err := s.ensureLoadedLocked(); err != nil {
        return 0, err
    }
    current := time.Now().UTC()
    cutoff := current.Add(-quotaHistoryRetention)
    retained := s.points[:0]
    for _, point := range s.points {
        timestamp, err := time.Parse(time.RFC3339Nano, point.Time)
        if err == nil && !timestamp.Before(cutoff) {
            retained = append(retained, point)
        }
    }
    pruned := len(retained) != len(s.points)
    s.points = retained
    added := make([]QuotaHistoryPoint, 0, len(request.Observations))
    for _, observation := range request.Observations {
        observation.AccountID = strings.TrimSpace(observation.AccountID)
        observation.WindowID = strings.TrimSpace(observation.WindowID)
        if observation.AccountID == "" || len(observation.AccountID) > 128 {
            return 0, errors.New("quota observation account_id is required and must not exceed 128 characters")
        }
        if observation.WindowID == "" || len(observation.WindowID) > 256 {
            return 0, errors.New("quota observation window_id is required and must not exceed 256 characters")
        }
        if observation.UsedPercentage != nil && (*observation.UsedPercentage < 0 || *observation.UsedPercentage > 100) {
            return 0, errors.New("quota used_percentage must be between 0 and 100")
        }
        id := observation.AccountID + "|" + observation.WindowID
        if last := latestQuotaPoint(s.points, id); last != nil {
            timestamp, _ := time.Parse(time.RFC3339Nano, last.Time)
            if current.Sub(timestamp) < 5*time.Minute && quotaPointEquivalent(*last, observation) {
                continue
            }
        }
        point := QuotaHistoryPoint{
            ID: id, Time: current.Format(time.RFC3339Nano), AccountID: observation.AccountID,
            Provider: strings.TrimSpace(observation.Provider), WindowID: observation.WindowID,
            Label: strings.TrimSpace(observation.Label), Used: observation.UsedPercentage,
            Remaining: observation.Remaining, Unit: strings.TrimSpace(observation.Unit),
            ResetAt: strings.TrimSpace(observation.ResetAt), ObservedAt: strings.TrimSpace(observation.ObservedAt),
            Source: strings.TrimSpace(observation.Source), Status: strings.TrimSpace(observation.Status),
        }
        s.points = append(s.points, point)
        added = append(added, point)
    }
    if len(added) == 0 && !pruned {
        return 0, nil
    }
    if err := os.MkdirAll(filepath.Dir(filepath.Clean(s.path)), 0o700); err != nil && filepath.Dir(filepath.Clean(s.path)) != "." {
        return 0, err
    }
    if pruned {
        if err := s.rewriteLocked(); err != nil {
            return 0, err
        }
        return len(added), nil
    }
    file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
    if err != nil {
        return 0, err
    }
    encoder := json.NewEncoder(file)
    for _, point := range added {
        if err := encoder.Encode(point); err != nil {
            _ = file.Close()
            return 0, err
        }
    }
    if err := file.Close(); err != nil {
        return 0, err
    }
    return len(added), nil
}

func latestQuotaPoint(points []QuotaHistoryPoint, id string) *QuotaHistoryPoint {
    for index := len(points) - 1; index >= 0; index-- {
        if points[index].ID == id {
            return &points[index]
        }
    }
    return nil
}

func quotaPointEquivalent(point QuotaHistoryPoint, input QuotaHistoryObservationInput) bool {
    if point.Status != strings.TrimSpace(input.Status) || point.ResetAt != strings.TrimSpace(input.ResetAt) {
        return false
    }
    if !optionalFloatClose(point.Used, input.UsedPercentage, 0.1) {
        return false
    }
    return optionalFloatClose(point.Remaining, input.Remaining, 0.001)
}

func optionalFloatClose(left, right *float64, tolerance float64) bool {
    if left == nil || right == nil {
        return left == nil && right == nil
    }
    difference := *left - *right
    if difference < 0 {
        difference = -difference
    }
    return difference <= tolerance
}

func (s *quotaHistoryStore) rewriteLocked() error {
    temporary := s.path + ".tmp"
    file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
    if err != nil {
        return err
    }
    encoder := json.NewEncoder(file)
    for _, point := range s.points {
        if err := encoder.Encode(point); err != nil {
            _ = file.Close()
            _ = os.Remove(temporary)
            return err
        }
    }
    if err := file.Close(); err != nil {
        _ = os.Remove(temporary)
        return err
    }
    return os.Rename(temporary, s.path)
}

func (s *quotaHistoryStore) query(duration time.Duration, accountID string) (map[string]any, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if err := s.ensureLoadedLocked(); err != nil {
        return nil, err
    }
    accountID = strings.TrimSpace(accountID)
    cutoff := time.Now().Add(-duration)
    result := make([]QuotaHistoryPoint, 0, len(s.points))
    for _, point := range s.points {
        timestamp, err := time.Parse(time.RFC3339Nano, point.Time)
        if err != nil || timestamp.Before(cutoff) || (accountID != "" && point.AccountID != accountID) {
            continue
        }
        result = append(result, point)
    }
    return map[string]any{
        "range_seconds": int64(duration.Seconds()), "retention_seconds": int64(quotaHistoryRetention.Seconds()),
        "data": result, "source": "server_observed_quota_history", "path": filepath.Base(s.path),
    }, nil
}

func parseQuotaHistoryRange(value string) (time.Duration, error) {
    switch strings.TrimSpace(value) {
    case "", "24h":
        return 24 * time.Hour, nil
    case "7d":
        return 7 * 24 * time.Hour, nil
    case "30d", "all":
        return quotaHistoryRetention, nil
    default:
        return 0, fmt.Errorf("quota history range must be one of 24h, 7d, 30d or all")
    }
}
'''

test_source = r'''package gateway

import (
    "path/filepath"
    "testing"
    "time"
)

func TestQuotaHistoryAppendDeduplicatesAndQueries(t *testing.T) {
    used := 42.5
    store := &quotaHistoryStore{path: filepath.Join(t.TempDir(), "quota.jsonl")}
    request := QuotaHistoryIngestRequest{Observations: []QuotaHistoryObservationInput{{
        AccountID: "account-a", Provider: "codex", WindowID: "five_hour", Label: "5 小时",
        UsedPercentage: &used, ResetAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Source: "provider",
    }}}
    added, err := store.append(request)
    if err != nil || added != 1 {
        t.Fatalf("first append = %d, %v", added, err)
    }
    added, err = store.append(request)
    if err != nil || added != 0 {
        t.Fatalf("duplicate append = %d, %v", added, err)
    }
    snapshot, err := store.query(24*time.Hour, "account-a")
    if err != nil {
        t.Fatal(err)
    }
    data, ok := snapshot["data"].([]QuotaHistoryPoint)
    if !ok || len(data) != 1 || data[0].Used == nil || *data[0].Used != used {
        t.Fatalf("unexpected snapshot: %#v", snapshot)
    }
}

func TestQuotaHistoryRejectsInvalidPercentage(t *testing.T) {
    used := 101.0
    store := &quotaHistoryStore{path: filepath.Join(t.TempDir(), "quota.jsonl")}
    _, err := store.append(QuotaHistoryIngestRequest{Observations: []QuotaHistoryObservationInput{{
        AccountID: "account-a", WindowID: "daily", UsedPercentage: &used,
    }}})
    if err == nil {
        t.Fatal("expected an invalid percentage error")
    }
}
'''

(gateway / 'quotahistory.go').write_text(go_source, encoding='utf-8')
(gateway / 'quotahistory_test.go').write_text(test_source, encoding='utf-8')

admin = admin_path.read_text(encoding='utf-8')
marker = '\tcase path == "/adapters" && r.Method == http.MethodGet:\n'
if marker not in admin:
    raise SystemExit('admin adapters marker was not found')
insert = '''\tcase path == "/quota-history" && r.Method == http.MethodGet:
\t\tduration, err := parseQuotaHistoryRange(r.URL.Query().Get("range"))
\t\tif err != nil {
\t\t\twriteAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_quota_history_range")
\t\t\treturn
\t\t}
\t\tsnapshot, err := adminQuotaHistory.query(duration, r.URL.Query().Get("account_id"))
\t\tif err != nil {
\t\t\twriteAPIErrorCode(w, http.StatusInternalServerError, "failed to read quota history", "gateway_error", "quota_history_read_failed")
\t\t\treturn
\t\t}
\t\twriteJSON(w, http.StatusOK, snapshot)
\tcase path == "/quota-history" && r.Method == http.MethodPost:
\t\tvar input QuotaHistoryIngestRequest
\t\tif err := decodeAdminJSON(w, r, &input); err != nil {
\t\t\treturn
\t\t}
\t\tstored, err := adminQuotaHistory.append(input)
\t\tif err != nil {
\t\t\twriteAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_quota_history")
\t\t\treturn
\t\t}
\t\twriteJSON(w, http.StatusOK, map[string]any{"ok": true, "stored": stored})
'''
if 'path == "/quota-history"' not in admin:
    admin = admin.replace(marker, insert + marker, 1)
admin_path.write_text(admin, encoding='utf-8')

app = app_path.read_text(encoding='utf-8')
old = "loadPersistedQuality();recordQuotaHistory();renderAll();"
new = "loadPersistedQuality();await syncQuotaHistory();renderAll();"
if old not in app:
    raise SystemExit('refresh quota-history marker was not found')
app = app.replace(old, new, 1)

marker = "function renderUsage(){"
if marker not in app:
    raise SystemExit('renderUsage marker was not found')
sync_function = r'''async function syncQuotaHistory(){
  const observations=APP.oauth.flatMap(account=>quotaWindows(account).map(window=>({
    account_id:account.id,provider:account.provider,window_id:[window.kind,window.model||'',window.label||''].join('|'),
    label:window.model||window.label||window.kind,used_percentage:quotaPercentage(window),
    remaining:Number.isFinite(Number(window.remaining))?Number(window.remaining):null,unit:window.unit||'',
    reset_at:window.reset_at||'',observed_at:window.observed_at||'',source:window.source||'',status:window.status||''
  })));
  try{
    if(observations.length)await request('/quota-history',{method:'POST',body:JSON.stringify({observations})});
    const history=await request(`/quota-history?range=${encodeURIComponent(APP.range)}`);
    APP.quotaHistory=history.data||[];
    try{localStorage.setItem('lite2api_quota_history_v1',JSON.stringify(APP.quotaHistory))}catch{}
  }catch(error){
    recordQuotaHistory();
    console.warn('server quota history unavailable; using browser fallback',error);
  }
}
'''
if 'async function syncQuotaHistory()' not in app:
    app = app.replace(marker, sync_function + marker, 1)
app = app.replace("APP.metric==='quota'?' · 浏览器持续保存额度快照':''", "APP.metric==='quota'?' · 服务端持续保存额度快照':''")
app_path.write_text(app, encoding='utf-8')

Path(__file__).unlink()
if workflow_path.exists():
    workflow_path.unlink()
