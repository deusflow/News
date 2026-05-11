package telegram

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseContentRangeTotal(t *testing.T) {
	size, err := parseContentRangeTotal("bytes 0-0/12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 12345 {
		t.Fatalf("expected 12345, got %d", size)
	}

	if _, err := parseContentRangeTotal("invalid"); err == nil {
		t.Fatalf("expected error for invalid content-range")
	}
}

func TestGetRemoteContentLength_HEAD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "555")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	size, err := GetRemoteContentLength(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 555 {
		t.Fatalf("expected 555, got %d", size)
	}
}

func TestGetRemoteContentLength_RangeFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-0/777")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	size, err := GetRemoteContentLength(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 777 {
		t.Fatalf("expected 777, got %d", size)
	}
}
