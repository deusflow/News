package embedding

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float64
		delta    float64
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 1.0,
			delta:    1e-5,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 0.0,
			delta:    1e-5,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{-1.0, 0.0},
			expected: -1.0,
			delta:    1e-5,
		},
		{
			name:     "empty vectors",
			a:        []float32{},
			b:        []float32{},
			expected: 0.0,
			delta:    0.0,
		},
		{
			name:     "mismatched length",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0,
			delta:    0.0,
		},
		{
			name:     "zero magnitude vector",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{1.0, 2.0, 3.0},
			expected: 0.0,
			delta:    0.0,
		},
		{
			name:     "arbitrary similar vectors",
			a:        []float32{1.0, 1.0, 0.0},
			b:        []float32{1.0, 1.0, 1.0},
			expected: 2.0 / (math.Sqrt(2.0) * math.Sqrt(3.0)), // ~0.8165
			delta:    1e-4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.expected) > tt.delta {
				t.Errorf("CosineSimilarity() = %v, expected %v (delta %v)", got, tt.expected, tt.delta)
			}
		})
	}
}

func TestClusterKeySimilarity(t *testing.T) {
	tests := []struct {
		name      string
		keyA      string
		keyB      string
		wantMin   float64
		wantMax   float64
		isFullDup bool
	}{
		{
			name:      "exact identical keys",
			keyA:      "ua-men-military-status-permit",
			keyB:      "ua-men-military-status-permit",
			wantMin:   1.0,
			wantMax:   1.0,
			isFullDup: true,
		},
		{
			name:      "same meaning with different order or slight variation",
			keyA:      "ua-men-military-status-permit",
			keyB:      "ua-military-status-permit-men",
			wantMin:   1.0,
			wantMax:   1.0,
			isFullDup: true,
		},
		{
			name:      "DR vs TV2 wording difference (4 out of 5 tokens match)",
			keyA:      "ua-men-military-status-permit",
			keyB:      "ua-men-military-status-rules",
			wantMin:   0.60,
			wantMax:   0.85,
			isFullDup: true, // overlaps >= 0.60
		},
		{
			name:      "completely different topic",
			keyA:      "ua-men-military-status-permit",
			keyB:      "dsb-train-delay-compensation",
			wantMin:   0.0,
			wantMax:   0.0,
			isFullDup: false,
		},
		{
			name:      "empty key",
			keyA:      "",
			keyB:      "dsb-train-delay-compensation",
			wantMin:   0.0,
			wantMax:   0.0,
			isFullDup: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClusterKeySimilarity(tt.keyA, tt.keyB)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ClusterKeySimilarity(%q, %q) = %v, expected between [%v, %v]",
					tt.keyA, tt.keyB, got, tt.wantMin, tt.wantMax)
			}
			isDup := got >= 0.60
			if isDup != tt.isFullDup {
				t.Errorf("ClusterKeySimilarity(%q, %q) isDup = %v, expected %v",
					tt.keyA, tt.keyB, isDup, tt.isFullDup)
			}
		})
	}
}
