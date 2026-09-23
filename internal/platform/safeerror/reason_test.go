package safeerror

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

type databaseFailure struct{ detail string }

func (e databaseFailure) Error() string    { return e.detail }
func (e databaseFailure) SQLState() string { return "23505" }

func TestReasonNeverIncludesErrorDetails(t *testing.T) {
	secret := "secret-sentinel-from-dependency"
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrapped: %w", context.Canceled), "cancelled"},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{&net.OpError{Op: "dial", Err: errors.New(secret)}, "network"},
		{fmt.Errorf("wrapped: %w", databaseFailure{detail: secret}), "database"},
		{errors.New(secret), "unexpected"},
	} {
		got := Reason(scenario.err)
		if got != scenario.want || strings.Contains(got, secret) {
			t.Fatalf("Reason(%T) = %q, want %q without secret", scenario.err, got, scenario.want)
		}
	}
}
