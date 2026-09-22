package reference

import (
	"testing"
	"time"
)

func TestBackoffCapsAndAppliesJitter(t *testing.T) {
	withoutJitter := func(time.Duration) time.Duration { return 0 }
	if got := backoff(1, withoutJitter); got != time.Second {
		t.Fatalf("first backoff = %s, want 1s", got)
	}
	if got := backoff(3, withoutJitter); got != 4*time.Second {
		t.Fatalf("third backoff = %s, want 4s", got)
	}
	if got := backoff(100, withoutJitter); got != 5*time.Minute {
		t.Fatalf("capped backoff = %s, want 5m", got)
	}
	if got := backoff(1, func(time.Duration) time.Duration { return 100 * time.Millisecond }); got != 1100*time.Millisecond {
		t.Fatalf("jittered backoff = %s, want 1.1s", got)
	}
}
