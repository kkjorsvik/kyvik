package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeID_GenerateAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.id")

	id1, err := loadOrCreateNodeID(path)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if id1 == "" {
		t.Fatal("node ID should not be empty")
	}

	id2, err := loadOrCreateNodeID(path)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if id1 != id2 {
		t.Errorf("expected %s, got %s", id1, id2)
	}

	data, _ := os.ReadFile(path)
	if string(data) != id1 {
		t.Errorf("file contents %q != %q", string(data), id1)
	}
}

func TestNodeID_CustomName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.id")

	id, err := loadOrCreateNodeID(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 36 {
		t.Errorf("expected UUID format, got %q", id)
	}
}
