package chatty

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBackendReservedRoutesBeforeAdapters(t *testing.T) {
	m, _, runner := fixture(t)
	calls := 0
	m.HTTP.Transport = roundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected adapter") })
	target, _ := url.Parse("http://127.0.0.1:8009")
	handler := m.doorHandler(target, "missing", log.New(io.Discard, "", 0))
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/_chatty/backend/unknown", 404},
		{"POST", "/_chatty/backend/v1/health", 405},
		{"GET", "/_chatty/backend/v1/health", 401},
	} {
		request := loopbackRequest("localhost:8008", test.path)
		request.Method = test.method
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("%s %s: got %d want %d", test.method, test.path, response.Code, test.status)
		}
	}
	if calls != 0 || len(runner.calls) != 0 {
		t.Fatalf("reserved routes reached adapters: HTTP=%d process=%v", calls, runner.calls)
	}
}
