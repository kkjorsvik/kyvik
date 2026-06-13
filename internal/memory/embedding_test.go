package memory

import (
	"math"
	"testing"
)

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1, 2, 3}
	sim := CosineSimilarity(a, a)
	if math.Abs(float64(sim)-1.0) > 1e-6 {
		t.Errorf("identical vectors: got %f, want 1.0", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim)) > 1e-6 {
		t.Errorf("orthogonal vectors: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim)+1.0) > 1e-6 {
		t.Errorf("opposite vectors: got %f, want -1.0", sim)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{0, 0, 0}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("zero vector: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different lengths: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	sim := CosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("empty vectors: got %f, want 0.0", sim)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}
	encoded := EncodeEmbedding(original)
	decoded := DecodeEmbedding(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("index %d: got %f, want %f", i, decoded[i], original[i])
		}
	}
}

func TestDecodeEmbedding_InvalidLength(t *testing.T) {
	// 5 bytes is not a multiple of 4
	result := DecodeEmbedding([]byte{1, 2, 3, 4, 5})
	if result != nil {
		t.Errorf("expected nil for invalid length, got %v", result)
	}
}

func TestEncodeDecodeEmpty(t *testing.T) {
	encoded := EncodeEmbedding(nil)
	if len(encoded) != 0 {
		t.Errorf("expected empty bytes, got %d bytes", len(encoded))
	}
	decoded := DecodeEmbedding(nil)
	if len(decoded) != 0 {
		t.Errorf("expected empty slice, got %d elements", len(decoded))
	}
}
