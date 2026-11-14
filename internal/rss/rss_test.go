package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAllFeedsDetailed_SetsBrowserUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Test</title><item><title>Hello</title><link>https://example.com/1</link></item></channel></rss>`))
	}))
	defer srv.Close()

	sources := []FeedSource{
		{Name: "TestFeed", URL: srv.URL, Active: true},
	}
	items, statuses, err := FetchAllFeedsDetailed(context.Background(), sources)
	if err != nil {
		t.Fatalf("FetchAllFeedsDetailed() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if statuses["TestFeed"].Err != nil {
		t.Fatalf("unexpected feed status error: %v", statuses["TestFeed"].Err)
	}
	if !strings.Contains(gotUA, "Mozilla/5.0") {
		t.Errorf("expected browser User-Agent, got %q", gotUA)
	}
}

