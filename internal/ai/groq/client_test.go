package groq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerate_ResponseFormatByProvider(t *testing.T) {
	tests := []struct {
		name               string
		providerName       string
		wantResponseFormat bool
	}{
		{
			name:               "groq includes json_object response_format",
			providerName:       "groq",
			wantResponseFormat: true,
		},
		{
			name:               "openrouter omits response_format for free models compatibility",
			providerName:       "openrouter",
			wantResponseFormat: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"danish\":\"Dansk\",\"ukrainian\":\"Укр\",\"tldr\":\"TLDR\",\"mood\":\"neutral\",\"audience_score\":5}"}}]}`))
			}))
			defer srv.Close()

			client := NewClientWithOptions(tt.providerName, "test-key", "test-model", srv.URL)
			resp, err := client.Generate(context.Background(), "Title", "Content", "sys", "user")
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if resp.Danish != "Dansk" {
				t.Fatalf("unexpected response: %+v", resp)
			}

			_, hasRespFmt := captured["response_format"]
			if hasRespFmt != tt.wantResponseFormat {
				t.Errorf("has response_format = %v, want %v (payload: %v)", hasRespFmt, tt.wantResponseFormat, captured)
			}
		})
	}
}

