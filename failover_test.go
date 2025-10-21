package failover

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTest = errors.New("test error")

func TestRetry_SuccessFirstTry(t *testing.T) {
	t.Parallel()
	fn := func() error {
		return nil
	}

	ctx := context.Background()

	err := Retry(ctx, 3, 10*time.Millisecond, fn)
	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}
}

func TestRetry_SuccessAfterFailures(t *testing.T) {
	t.Parallel()
	attempts := 0

	fn := func() error {
		attempts++
		if attempts < 3 {
			return errTest
		}

		return nil
	}

	ctx := context.Background()
	err := Retry(ctx, 5, 10*time.Millisecond, fn)
	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected  3 attempts, got %d", attempts)
	}
}

func TestRetry_FailAllAttempts(t *testing.T) {
	t.Parallel()
	attempts := 0

	fn := func() error {
		attempts++
		return errTest
	}

	ctx := context.Background()
	err := Retry(ctx, 3, 10*time.Millisecond, fn)

	if err == nil {
		t.Error("Expected an error, got nil")
	}

	if !errors.Is(err, errTest) {
		t.Errorf("Expected error %v, got %v", errTest, err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestRetry_ContextTimeout(t *testing.T) {
	t.Parallel()

	slowFn := func() error {
		time.Sleep(100 * time.Millisecond)

		return errTest
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := Retry(ctx, 5, 10*time.Millisecond, slowFn)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

// --- Test CircuitBreaker ---
// TestCircuitBreaker_Flow tests the full lifecycle of the breaker:
// Closed -> Open -> HalfOpen -> Closed
func TestCircuitBreaker_Flow(t *testing.T) {
	// failureThreshold: 2, successThreshold: 2, openTimeout: 100ms
	cb := NewCircuitBreaker(2, 2, 100*time.Millisecond)

	succeed := func() error { return nil }
	fail := func() error { return errTest }

	// -- 1. Closed state
	if err := cb.Execute(succeed); err != nil {
		t.Fatalf("State Closed: Expected nil error, got %v", err)
	}
	if cb.state != Closed {
		t.Fatalf("State Closed: Expected state Closed, got %v", cb.state)
	}

	// --- 2. Trip to Open
	if err := cb.Execute(fail); !errors.Is(err, errTest) {
		t.Fatalf("State Closed->Open: Expected test error, got %v", err)
	}
	if cb.state != Closed {
		t.Fatalf("State Closed->Open: Expected state Closed after 1 failure, got %v", cb.state)
	}

	// Fail 2 (this should trip the breaker)

	if err := cb.Execute(fail); !errors.Is(err, errTest) {
		t.Fatalf("State Closed->Open: Expected test error 2nd failure, got %v", err)
	}
	if cb.state != Open {
		t.Fatalf("State Closed->Open: Expected test error on 2nd failure, got %v", cb.state)
	}

	// --- 4. Move to Half-Open ---
	time.Sleep(110 * time.Millisecond) // Wait for openTimeout to expire

	// --- 5. Half-Open to Open ---
	// This first call will be allowed (as a test). Let's make it fail.
	if err := cb.Execute(fail); !errors.Is(err, errTest) {
		t.Fatalf("State HalfOpen->Open: Expected test error, got %v", err)
	}
	if cb.state != Open {
		t.Fatalf("State HalfOpen->Open: Expected state Open after failure, got %v", cb.state)
	}

	// --- 6. Move to Half-Open (again) ---
	time.Sleep(110 * time.Millisecond) // Wait for timeout again

	// --- 7. Half-Open to Closed ---
	// Now let's succeed
	// Success 1
	if err := cb.Execute(succeed); err != nil {
		t.Fatalf("State HalfOpen->Closed: Expected nil error on 1st success, got %v", err)
	}
	if cb.state != HalfOpen { // Not yet closed
		t.Fatalf("State HalfOpen->Closed: Expected state HalfOpen, got %v", cb.state)
	}
	if cb.successCount != 1 {
		t.Fatalf("State HalfOpen->Closed: Expected successCount 1, got %d", cb.successCount)
	}

	// Success 2 (This should close the circuit)
	if err := cb.Execute(succeed); err != nil {
		t.Fatalf("State HalfOpen->Closed: Expected nil error on 2nd success, got %v", err)
	}
	if cb.state != Closed {
		t.Fatalf("State HalfOpen->Closed: Expected state Closed, got %v", cb.state)
	}
	if cb.failureCount != 0 {
		t.Fatalf("State HalfOpen->Closed: Expected failureCount to be 0, got %d", cb.failureCount)
	}

	// --- 8. Back to Closed ---
	// Verify it's working normally again
	if err := cb.Execute(succeed); err != nil {
		t.Fatalf("State Closed (final): Expected nil error, got %v", err)
	}
	if cb.state != Closed {
		t.Fatalf("State Closed (final): Expected state Closed, got %v", cb.state)
	}
}

func TestCircuitBreaker_ResetOnSuccessInClosed(t *testing.T) {
	t.Parallel()
	cb := NewCircuitBreaker(3, 1, 1*time.Minute) // 3 failures to trip

	fail := func() error { return errTest }
	succeed := func() error { return nil }

	// Fail 1
	_ = cb.Execute(fail)
	if cb.failureCount != 1 {
		t.Fatalf("Expected failureCount 1, got %d", cb.failureCount)
	}

	// Fail 2
	_ = cb.Execute(fail)
	if cb.failureCount != 2 {
		t.Fatalf("Expected failureCount 2, got %d", cb.failureCount)
	}

	// Success (should reset counter)
	_ = cb.Execute(succeed)
	if cb.failureCount != 0 {
		t.Fatalf("Expected failureCount to reset to 0 after success, got %d", cb.failureCount)
	}
	if cb.state != Closed {
		t.Fatalf("Expected state to remain Closed, got %v", cb.state)
	}

	// Fail 3 (should not trip, since counter was reset)
	_ = cb.Execute(fail)
	if cb.failureCount != 1 {
		t.Fatalf("Expected failureCount 1, got %d", cb.failureCount)
	}
	if cb.state != Closed {
		t.Fatalf("Expected state to remain Closed, got %v", cb.state)
	}
}
