package provider

import (
	"sync"
	"time"
)

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

type CircuitBreaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	state    CircuitState
	failures int
	openedAt time.Time
}

type CircuitSnapshot struct {
	State    CircuitState `json:"state"`
	Failures int          `json:"failures"`
	OpenedAt *time.Time   `json:"opened_at,omitempty"`
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold < 1 {
		threshold = 1
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
		state:     CircuitClosed,
	}
}

func (c *CircuitBreaker) Allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != CircuitOpen {
		return true
	}
	if c.now().Sub(c.openedAt) >= c.cooldown {
		c.state = CircuitHalfOpen
		return true
	}
	return false
}

func (c *CircuitBreaker) Record(success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if success {
		c.failures = 0
		c.openedAt = time.Time{}
		c.state = CircuitClosed
		return
	}

	c.failures++
	if c.state == CircuitHalfOpen || c.failures >= c.threshold {
		c.state = CircuitOpen
		c.openedAt = c.now()
	}
}

func (c *CircuitBreaker) Snapshot() CircuitSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := CircuitSnapshot{
		State:    c.state,
		Failures: c.failures,
	}
	if !c.openedAt.IsZero() {
		openedAt := c.openedAt
		snapshot.OpenedAt = &openedAt
	}
	return snapshot
}
