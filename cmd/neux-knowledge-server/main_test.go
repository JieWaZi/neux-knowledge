package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestThinHostRejectsMissingAndUnauthenticatedConfiguration(t *testing.T) {
	if err := run(context.Background(), nil, io.Discard); err == nil {
		t.Fatal("started without explicit configuration")
	}
	p := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(p, []byte(`{"dbPath":"db","rawDir":"raw","security":{"tokens":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"-config", p}, io.Discard); err == nil {
		t.Fatal("started without credentials")
	}
}
