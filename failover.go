package failover

import (
	"context"
	"errors"
	"sync"
	"time"
)

// WorkFunc is a simple function signature for operations that can fail.
type WorkFunc func() error

// Retry executes a WorkFunc, retrying it on failure.
// It uses exponential backoff for delays between retries.
func Retry(ctx context.Context, attempts int, initialDelay time.Duration, fn WorkFunc) error {
	var err error
	delay := initialDelay

	for i := range attempts {
		select {
		case <-ctx.Done():
			return ctx.Err()

		default:
			// context is not done, proceed.
		}

		err = fn()

		if err == nil {
			return nil // success
		}

		// last attempt
		if i == attempts-1 {
			break
		}

		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return err
}

type State int

const (
	// Closed allows operation to execute.
	Closed State = iota
	// Open rejects operation immediately
	Open
	// HalfOpen allows a single test operation.
	HalfOpen
)

// ErrCircuitOpen is returned  when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker holds the state of the breaker.
type CircuitBreaker struct {
	mu sync.Mutex // Protects the state fields

	state            State
	failureThreshold int // How many failures to trip to Open
	successThreshold int // How many success in HalfOpen to Closed
	openTimeout      time.Duration

	failureCount    int
	successCount    int
	lastFailureTime time.Time
}

// NewCircuitBreaker creates a new CircuitBreaker with default settings.
func NewCircuitBreaker(failureThreshold, successThreshold int, openTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            Closed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		openTimeout:      openTimeout,
	}
}

// Execute wraps a function call with the circuit breaker logic.
func (cb *CircuitBreaker) Execute(fn WorkFunc) error {
	cb.mu.Lock()

	if cb.state == Open {
		if time.Since(cb.lastFailureTime) > cb.openTimeout {
			cb.state = HalfOpen
			cb.successCount = 0

		} else {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
	}

	cb.mu.Unlock()

	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		return cb.onSuccess()
	}

	cb.onFailure()
	return err
}

// onSuccess handles a successful call.
func (cb *CircuitBreaker) onSuccess() error {
	switch cb.state {
	case HalfOpen:
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = Closed
			cb.failureCount = 0
		}
	case Closed:
		cb.failureCount = 0
	}

	return nil
}

func (cb *CircuitBreaker) onFailure() error {
	switch cb.state {
	case HalfOpen:
		cb.state = Open
		cb.lastFailureTime = time.Now()
	case Closed:
		cb.failureCount++
		if cb.failureCount >= cb.failureThreshold {
			cb.state = Open
			cb.lastFailureTime = time.Now()
		}
	}
	return nil
}
