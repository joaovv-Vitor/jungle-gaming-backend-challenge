package safeerror

import (
	"context"
	"errors"
	"net"
)

// Reason returns a bounded operational category. Error messages can contain
// credentials, request data, or database values and must not reach logs or the
// outbox retry record verbatim.
func Reason(err error) string {
	if err == nil {
		return "none"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout"
		}
		return "network"
	}
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) {
		return "database"
	}
	return "unexpected"
}
