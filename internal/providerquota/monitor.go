package providerquota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linlay/transit-hub/internal/config"
)

const (
	requestTimeout   = 15 * time.Second
	maxResponseBytes = 1 << 20
	maxConcurrency   = 4
)

type AccountSnapshot struct {
	Provider        string     `json:"provider"`
	Pool            string     `json:"pool"`
	Account         string     `json:"account"`
	IntervalSeconds int64      `json:"interval_seconds"`
	State           string     `json:"state"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Entries         []Entry    `json:"entries"`
}

type Snapshot struct {
	Items []AccountSnapshot `json:"items"`
}

type Options struct {
	Client           *http.Client
	Logger           *log.Logger
	Now              func() time.Time
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

type target struct {
	id              string
	provider        string
	pool            string
	account         string
	apiKey          string
	requestIdentity string
	configIdentity  string
	quota           config.ProviderQuotaConfig
	interval        time.Duration
	nextDue         time.Time
}

type Monitor struct {
	mu               sync.RWMutex
	targets          map[string]target
	snapshots        map[string]AccountSnapshot
	client           *http.Client
	logger           *log.Logger
	now              func() time.Time
	requestTimeout   time.Duration
	maxResponseBytes int64
	wake             chan struct{}
	cancel           context.CancelFunc
	done             chan struct{}
}

func New(configs []config.ProviderConfig, options Options) (*Monitor, error) {
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	logger := options.Logger
	if logger == nil {
		logger = log.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	monitor := &Monitor{
		targets:          map[string]target{},
		snapshots:        map[string]AccountSnapshot{},
		client:           client,
		logger:           logger,
		now:              now,
		requestTimeout:   options.RequestTimeout,
		maxResponseBytes: options.MaxResponseBytes,
		wake:             make(chan struct{}, 1),
		done:             make(chan struct{}),
	}
	if monitor.requestTimeout <= 0 {
		monitor.requestTimeout = requestTimeout
	}
	if monitor.maxResponseBytes <= 0 {
		monitor.maxResponseBytes = maxResponseBytes
	}
	if err := monitor.replace(configs, false); err != nil {
		return nil, err
	}
	return monitor, nil
}

func (m *Monitor) Start(ctx context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()
	go m.run(runCtx)
}

func (m *Monitor) Replace(configs []config.ProviderConfig) error {
	if err := m.replace(configs, true); err != nil {
		return err
	}
	m.signalWake()
	return nil
}

func (m *Monitor) replace(configs []config.ProviderConfig, preserve bool) error {
	targets, snapshots, err := buildTargets(configs, m.now().UTC())
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if preserve {
		for id, next := range targets {
			previous, ok := m.targets[id]
			if !ok || previous.configIdentity != next.configIdentity {
				continue
			}
			next.nextDue = previous.nextDue
			targets[id] = next
			if snapshot, ok := m.snapshots[id]; ok {
				snapshots[id] = snapshot
			}
		}
	}
	m.targets = targets
	m.snapshots = snapshots
	return nil
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	items := make([]AccountSnapshot, 0, len(m.snapshots))
	for _, snapshot := range m.snapshots {
		copySnapshot := snapshot
		copySnapshot.Entries = make([]Entry, len(snapshot.Entries))
		for index, entry := range snapshot.Entries {
			copySnapshot.Entries[index] = Entry{Title: entry.Title, Lines: append([]string(nil), entry.Lines...)}
		}
		items = append(items, copySnapshot)
	}
	m.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Provider != items[j].Provider {
			return items[i].Provider < items[j].Provider
		}
		if items[i].Pool != items[j].Pool {
			return items[i].Pool < items[j].Pool
		}
		return items[i].Account < items[j].Account
	})
	return Snapshot{Items: items}
}

func (m *Monitor) Close(ctx context.Context) error {
	m.mu.RLock()
	cancel := m.cancel
	done := m.done
	m.mu.RUnlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Monitor) run(ctx context.Context) {
	defer close(m.done)
	for {
		wait, hasTargets := m.nextWait()
		var timer *time.Timer
		var timerC <-chan time.Time
		if hasTargets {
			timer = time.NewTimer(wait)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-m.wake:
			stopTimer(timer)
			continue
		case <-timerC:
			m.sweep(ctx)
		}
	}
}

func (m *Monitor) nextWait() (time.Duration, bool) {
	now := m.now().UTC()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.targets) == 0 {
		return 0, false
	}
	var earliest time.Time
	for _, target := range m.targets {
		if earliest.IsZero() || target.nextDue.Before(earliest) {
			earliest = target.nextDue
		}
	}
	if !earliest.After(now) {
		return 0, true
	}
	return earliest.Sub(now), true
}

func (m *Monitor) sweep(ctx context.Context) {
	now := m.now().UTC()
	groups := map[string][]target{}
	m.mu.Lock()
	for id, current := range m.targets {
		if current.nextDue.After(now) {
			continue
		}
		groups[current.requestIdentity] = append(groups[current.requestIdentity], current)
		current.nextDue = now.Add(current.interval)
		m.targets[id] = current
	}
	m.mu.Unlock()

	semaphore := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, targets := range groups {
		targets := targets
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			m.queryGroup(ctx, targets)
		}()
	}
	wg.Wait()
}

func (m *Monitor) queryGroup(ctx context.Context, targets []target) {
	if len(targets) == 0 {
		return
	}
	body, err := m.fetch(ctx, targets[0])
	attemptAt := m.now().UTC()
	for _, current := range targets {
		if err != nil {
			m.applyError(current, attemptAt, err.Error())
			continue
		}
		entries, parseErr := parseEntries(body, current.quota)
		if parseErr != nil {
			m.applyError(current, attemptAt, parseErr.Error())
			continue
		}
		m.applySuccess(current, attemptAt, entries)
	}
}

func (m *Monitor) fetch(ctx context.Context, target target) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, m.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.quota.URL, nil)
	if err != nil {
		return nil, errors.New("build upstream request failed")
	}
	req.Header.Set("Authorization", "Bearer "+target.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("upstream request timed out")
		}
		if errors.Is(requestCtx.Err(), context.Canceled) {
			return nil, errors.New("upstream request canceled")
		}
		return nil, errors.New("upstream request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, m.maxResponseBytes))
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, m.maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("read upstream response failed")
	}
	if int64(len(body)) > m.maxResponseBytes {
		return nil, errors.New("upstream response exceeded configured limit")
	}
	return body, nil
}

func (m *Monitor) applyError(target target, at time.Time, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.targets[target.id]
	if !ok || current.configIdentity != target.configIdentity {
		return
	}
	snapshot := m.snapshots[target.id]
	snapshot.State = "error"
	snapshot.LastAttemptAt = timePointer(at)
	snapshot.LastError = message
	m.snapshots[target.id] = snapshot
	m.logger.Printf("provider quota query failed for provider=%q pool=%q account=%q: %s", target.provider, target.pool, target.account, message)
}

func (m *Monitor) applySuccess(target target, at time.Time, entries []Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.targets[target.id]
	if !ok || current.configIdentity != target.configIdentity {
		return
	}
	snapshot := m.snapshots[target.id]
	snapshot.State = "ok"
	snapshot.LastAttemptAt = timePointer(at)
	snapshot.LastSuccessAt = timePointer(at)
	snapshot.LastError = ""
	snapshot.Entries = make([]Entry, len(entries))
	for index, entry := range entries {
		snapshot.Entries[index] = Entry{Title: entry.Title, Lines: append([]string(nil), entry.Lines...)}
	}
	m.snapshots[target.id] = snapshot
}

func buildTargets(configs []config.ProviderConfig, now time.Time) (map[string]target, map[string]AccountSnapshot, error) {
	targets := map[string]target{}
	snapshots := map[string]AccountSnapshot{}
	for _, provider := range configs {
		if provider.Quota == nil {
			continue
		}
		quotaConfig := *provider.Quota
		quotaConfig.URL = strings.TrimSpace(quotaConfig.URL)
		interval, err := config.ProviderQuotaInterval(quotaConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("provider %q quota: %w", provider.Name, err)
		}
		for _, pool := range provider.Pools {
			for _, account := range pool.Accounts {
				id := strings.Join([]string{provider.Name, pool.Name, account.Name}, "\x00")
				requestIdentity := fingerprint(quotaConfig.URL, account.APIKey)
				configIdentity := quotaConfigIdentity(quotaConfig, interval, account.APIKey)
				targets[id] = target{
					id:              id,
					provider:        provider.Name,
					pool:            pool.Name,
					account:         account.Name,
					apiKey:          account.APIKey,
					requestIdentity: requestIdentity,
					configIdentity:  configIdentity,
					quota:           quotaConfig,
					interval:        interval,
					nextDue:         now,
				}
				snapshots[id] = AccountSnapshot{
					Provider:        provider.Name,
					Pool:            pool.Name,
					Account:         account.Name,
					IntervalSeconds: int64(interval / time.Second),
					State:           "pending",
					Entries:         []Entry{},
				}
			}
		}
	}
	return targets, snapshots, nil
}

func quotaConfigIdentity(cfg config.ProviderQuotaConfig, interval time.Duration, apiKey string) string {
	parts := []string{strings.TrimSpace(cfg.URL), interval.String(), strings.TrimSpace(cfg.ItemsPath), apiKey}
	fieldNames := make([]string, 0, len(cfg.Fields))
	for name := range cfg.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		parts = append(parts, "field", name, cfg.Fields[name])
	}
	parts = append(parts, "display-title", cfg.Display.Title)
	for _, line := range cfg.Display.Lines {
		parts = append(parts, "display-line", line)
	}
	valueMapNames := make([]string, 0, len(cfg.Display.ValueMaps))
	for name := range cfg.Display.ValueMaps {
		valueMapNames = append(valueMapNames, name)
	}
	sort.Strings(valueMapNames)
	for _, name := range valueMapNames {
		valueNames := make([]string, 0, len(cfg.Display.ValueMaps[name]))
		for raw := range cfg.Display.ValueMaps[name] {
			valueNames = append(valueNames, raw)
		}
		sort.Strings(valueNames)
		for _, raw := range valueNames {
			parts = append(parts, "value-map", name, raw, cfg.Display.ValueMaps[name][raw])
		}
	}
	return fingerprint(parts...)
}

func fingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func timePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (m *Monitor) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
