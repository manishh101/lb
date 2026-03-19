package health

import (
	"sync"
	"time"
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed   State = iota // Normal operation — requests flow through
	StateOpen                  // Circuit tripped — requests blocked
	StateHalfOpen              // Recovery probe — one test request allowed
)

// String returns a human-readable circuit breaker state name.
func (s State) String() string {
	return [...]string{"CLOSED", "OPEN", "HALF_OPEN"}[s]
}

// Breaker implements a per-server circuit breaker with three states:
// CLOSED (normal), OPEN (blocking), HALF_OPEN (probe).
type Breaker struct {
	mu              sync.Mutex
	state           State
	failureCount    int
	threshold       int
	recoveryTimeout time.Duration
	lastFailureTime time.Time
}

// NewBreaker creates a circuit breaker with the given failure threshold
// and recovery timeout duration.
func NewBreaker(threshold int, recoveryTimeout time.Duration) *Breaker {
	return &Breaker{
		state:           StateClosed,
		threshold:       threshold,
		recoveryTimeout: recoveryTimeout,
	}
}

// IsOpen is a pure read — no state transitions. Used for candidate filtering
// in the routing loop. (FIX B5: never call CanSend() in a loop)
func (b *Breaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != StateOpen {
		return false
	}
	// Still within recovery timeout → truly open
	return time.Since(b.lastFailureTime) <= b.recoveryTimeout
}

// CanSend may transition OPEN → HALF_OPEN. Only call on the single chosen
// server after selection — never inside a filtering loop.
func (b *Breaker) CanSend() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(b.lastFailureTime) > b.recoveryTimeout {
			b.state = StateHalfOpen
			return true // allow one probe
		}
		return false
	case StateHalfOpen:
		return true
	}
	return false
}

// RecordSuccess resets the breaker to CLOSED state.
// Returns true if the state transitioned from OPEN or HALF_OPEN.
func (b *Breaker) RecordSuccess() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	wasOpen := b.state != StateClosed
	b.failureCount = 0
	b.state = StateClosed
	return wasOpen
}

// RecordFailure increments the failure counter and trips the breaker
// if the threshold is reached or if the breaker was in HALF_OPEN.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failureCount++
	b.lastFailureTime = time.Now()
	if b.failureCount >= b.threshold || b.state == StateHalfOpen {
		b.state = StateOpen
	}
}

// State returns the current circuit breaker state as a string.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state.String()
}
