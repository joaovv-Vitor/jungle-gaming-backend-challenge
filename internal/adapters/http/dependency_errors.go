package httpadapter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// A failed or uncertain database round trip must not be reported as a
// definitive business failure. Clients can retry with the same identity.
func writeDependencyOrInternalError(w http.ResponseWriter, err error) {
	if isTransientDependencyError(err) {
		writeAPIError(w, http.StatusServiceUnavailable, "TRANSIENT_FAILURE")
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
}

func isTransientDependencyError(err error) bool {
	if errors.Is(err, pgconn.ErrConnClosed) || pgconn.SafeToRetry(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return len(databaseError.Code) >= 2 &&
			(databaseError.Code[:2] == "08" || databaseError.Code[:2] == "40" || databaseError.Code[:2] == "53" ||
				databaseError.Code == "57P01" || databaseError.Code == "57P02" || databaseError.Code == "57P03")
	}
	return false
}
