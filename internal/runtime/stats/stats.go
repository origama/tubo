package stats

import (
	"sync"
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
	snap Snapshot
}

func New(meta Snapshot) *Collector {
	c := &Collector{}
	c.snap = meta
	c.snap.CollectedAt = time.Now().UTC()
	return c
}

func (c *Collector) SetMeta(meta Snapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.Role = meta.Role
	c.snap.Kind = meta.Kind
	c.snap.Service = meta.Service
	c.snap.ServiceID = meta.ServiceID
	c.snap.Path = meta.Path
	c.snap.Status = meta.Status
	c.snap.Reason = meta.Reason
	c.snap.CollectedAt = time.Now().UTC()
	c.mu.Unlock()
}

func (c *Collector) Begin() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.Active++
	now := time.Now().UTC()
	c.snap.CollectedAt = now
	c.snap.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) Finish(err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.snap.Active > 0 {
		c.snap.Active--
	}
	if err != nil {
		c.snap.Errors++
	} else {
		c.snap.Completed++
	}
	now := time.Now().UTC()
	c.snap.CollectedAt = now
	c.snap.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) AddRx(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.mu.Lock()
	c.snap.RxBytesTotal += n
	now := time.Now().UTC()
	c.snap.CollectedAt = now
	c.snap.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) AddTx(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.mu.Lock()
	c.snap.TxBytesTotal += n
	now := time.Now().UTC()
	c.snap.CollectedAt = now
	c.snap.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) Observe(statusCode int, latency time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.RequestsTotal++
	c.snap.LastStatusCode = statusCode
	c.snap.LastLatencyMS = latency.Milliseconds()
	now := time.Now().UTC()
	c.snap.CollectedAt = now
	c.snap.LastActivityAt = &now
	c.mu.Unlock()
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.snap
	if snap.LastActivityAt != nil {
		t := *snap.LastActivityAt
		snap.LastActivityAt = &t
	}
	return snap
}
