package stats

import (
	"sync"
	"sync/atomic"
	"time"
)

type Snapshot struct {
	CollectedAt    time.Time  `json:"collected_at"`
	Role           string     `json:"role,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	Service        string     `json:"service,omitempty"`
	ServiceID      string     `json:"service_id,omitempty"`
	Path           string     `json:"path,omitempty"`
	Status         string     `json:"status,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	RxBytesTotal   int64      `json:"rx_bytes_total,omitempty"`
	TxBytesTotal   int64      `json:"tx_bytes_total,omitempty"`
	Active         int64      `json:"active,omitempty"`
	Completed      int64      `json:"completed,omitempty"`
	Errors         int64      `json:"errors,omitempty"`
	RequestsTotal  int64      `json:"requests_total,omitempty"`
	LastStatusCode int        `json:"last_status_code,omitempty"`
	LastLatencyMS  int64      `json:"last_latency_ms,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
}

type Collector struct {
	mu   sync.Mutex
	meta Snapshot

	rxBytesTotal atomic.Int64
	txBytesTotal atomic.Int64
	active       atomic.Int64
	completed    atomic.Int64
	errors       atomic.Int64
	requests     atomic.Int64
}

func New(meta Snapshot) *Collector {
	c := &Collector{}
	c.setMeta(meta)
	return c
}

func (c *Collector) setMeta(meta Snapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.meta.Role = meta.Role
	c.meta.Kind = meta.Kind
	c.meta.Service = meta.Service
	c.meta.ServiceID = meta.ServiceID
	c.meta.Path = meta.Path
	c.meta.Status = meta.Status
	c.meta.Reason = meta.Reason
	c.meta.CollectedAt = time.Now().UTC()
	c.mu.Unlock()
}

func (c *Collector) SetMeta(meta Snapshot) {
	c.setMeta(meta)
}

func (c *Collector) Begin() {
	if c == nil {
		return
	}
	c.active.Add(1)
	c.markActivity()
}

func (c *Collector) Finish(err error) {
	if c == nil {
		return
	}
	for {
		current := c.active.Load()
		if current == 0 {
			break
		}
		if c.active.CompareAndSwap(current, current-1) {
			break
		}
	}
	if err != nil {
		c.errors.Add(1)
	} else {
		c.completed.Add(1)
	}
	c.markActivity()
}

func (c *Collector) AddRx(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.rxBytesTotal.Add(n)
}

func (c *Collector) AddTx(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.txBytesTotal.Add(n)
}

func (c *Collector) Observe(statusCode int, latency time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.meta.LastStatusCode = statusCode
	c.meta.LastLatencyMS = latency.Milliseconds()
	c.meta.CollectedAt = time.Now().UTC()
	now := c.meta.CollectedAt
	c.meta.LastActivityAt = &now
	c.mu.Unlock()
	c.requests.Add(1)
}

func (c *Collector) markActivity() {
	if c == nil {
		return
	}
	c.mu.Lock()
	now := time.Now().UTC()
	c.meta.CollectedAt = now
	c.meta.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	snap := c.meta
	if snap.LastActivityAt != nil {
		t := *snap.LastActivityAt
		snap.LastActivityAt = &t
	}
	c.mu.Unlock()

	snap.RxBytesTotal = c.rxBytesTotal.Load()
	snap.TxBytesTotal = c.txBytesTotal.Load()
	snap.Active = c.active.Load()
	snap.Completed = c.completed.Load()
	snap.Errors = c.errors.Load()
	snap.RequestsTotal = c.requests.Load()
	return snap
}
