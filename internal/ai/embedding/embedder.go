package embedding

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/deusflow/News/internal/logger"
	"google.golang.org/genai"
)

// Embedder generates vector embeddings for text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// GeminiEmbedder implements Embedder using Google Gemini (e.g. text-embedding-004) with key rotation.
type GeminiEmbedder struct {
	clients   []*genai.Client
	modelName string
	keyIdx    int
	mu        sync.Mutex
}

// NewGeminiEmbedder initializes a new GeminiEmbedder with key rotation across provided API keys.
func NewGeminiEmbedder(apiKeys []string, modelName string) (*GeminiEmbedder, error) {
	if modelName == "" {
		modelName = "text-embedding-004"
	}
	ctx := context.Background()
	var clients []*genai.Client

	for _, key := range apiKeys {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		c, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey:  k,
			Backend: genai.BackendGeminiAPI,
		})
		if err != nil {
			logger.Warn("Failed to create gemini embedding client for key", "error", err)
			continue
		}
		clients = append(clients, c)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("no valid gemini api keys for embedder")
	}

	return &GeminiEmbedder{
		clients:   clients,
		modelName: modelName,
	}, nil
}

// Embed generates an embedding vector for the given text.
func (e *GeminiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty text for embedding")
	}

	e.mu.Lock()
	attempts := len(e.clients)
	e.mu.Unlock()

	var lastErr error
	for i := 0; i < attempts; i++ {
		e.mu.Lock()
		client := e.clients[e.keyIdx]
		e.keyIdx = (e.keyIdx + 1) % len(e.clients)
		e.mu.Unlock()

		resp, err := client.Models.EmbedContent(ctx, e.modelName, genai.Text(text), nil)
		if err != nil {
			lastErr = err
			logger.Warn("Gemini EmbedContent attempt failed", "attempt", i+1, "error", err)
			continue
		}
		if resp == nil || len(resp.Embeddings) == 0 || len(resp.Embeddings[0].Values) == 0 {
			lastErr = fmt.Errorf("empty embedding returned")
			continue
		}
		return resp.Embeddings[0].Values, nil
	}

	return nil, fmt.Errorf("all gemini embedding attempts failed: %w", lastErr)
}

// CosineSimilarity computes cosine similarity between two float vectors.
// Returns value in [-1.0, 1.0]. Returns 0.0 if vectors differ in length or have zero magnitude.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0.0
	}

	var dot, normA, normB float64
	for i := 0; i < len(a); i++ {
		valA := float64(a[i])
		valB := float64(b[i])
		dot += valA * valB
		normA += valA * valA
		normB += valB * valB
	}

	if normA == 0.0 || normB == 0.0 {
		return 0.0
	}

	sim := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	// Clamp floating precision artifacts
	if sim > 1.0 {
		return 1.0
	}
	if sim < -1.0 {
		return -1.0
	}
	return sim
}

// TokenizeClusterKey extracts normalized lowercase alphanumeric tokens from a cluster key string.
func TokenizeClusterKey(key string) map[string]struct{} {
	tokens := make(map[string]struct{})
	var current strings.Builder

	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 {
				tokens[current.String()] = struct{}{}
			}
			current.Reset()
		}
	}
	if current.Len() >= 2 {
		tokens[current.String()] = struct{}{}
	}

	return tokens
}

// ClusterKeySimilarity computes Jaccard similarity between two cluster keys based on token overlap.
func ClusterKeySimilarity(keyA, keyB string) float64 {
	normA := strings.TrimSpace(strings.ToLower(keyA))
	normB := strings.TrimSpace(strings.ToLower(keyB))

	if normA == "" || normB == "" {
		return 0.0
	}
	if normA == normB {
		return 1.0
	}

	tokensA := TokenizeClusterKey(normA)
	tokensB := TokenizeClusterKey(normB)

	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0.0
	}

	intersection := 0
	for tok := range tokensA {
		if _, ok := tokensB[tok]; ok {
			intersection++
		}
	}

	union := len(tokensA) + len(tokensB) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}
