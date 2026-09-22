package httpadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDependencyErrorsAreRetriableWithoutClaimingFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"deadline", fmt.Errorf("begin: %w", context.DeadlineExceeded), http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"reset", fmt.Errorf("query: %w", syscall.ECONNRESET), http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"eof", fmt.Errorf("commit: %w", io.ErrUnexpectedEOF), http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"closed pgx connection", fmt.Errorf("begin: %w", pgconn.ErrConnClosed), http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"server shutdown", &pgconn.PgError{Code: "57P01"}, http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"serialization", &pgconn.PgError{Code: "40001"}, http.StatusServiceUnavailable, "TRANSIENT_FAILURE"},
		{"constraint", &pgconn.PgError{Code: "23505"}, http.StatusInternalServerError, "INTERNAL_ERROR"},
		{"programming", errors.New("unexpected mapping"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeDependencyOrInternalError(response, test.err)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body.String(), test.wantStatus, test.wantCode)
			}
			if test.wantStatus == http.StatusServiceUnavailable && wagerErrorMetric(test.err) != "dependency_unavailable" {
				t.Fatalf("metric result = %s, want dependency_unavailable", wagerErrorMetric(test.err))
			}
		})
	}
}
