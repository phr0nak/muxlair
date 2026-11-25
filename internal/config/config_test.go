package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "config.json")
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	o, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil || o != Defaults() {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	// A config from before v0.5.0 still loads: the removed auto-delete key is ignored.
	if o, err = Load(write(`{"auto_delete_headless":"soon","mouse":false}`)); err != nil || o.Mouse || !o.Details {
		t.Fatalf("old key: %+v %v", o, err)
	}
	if o, _ = Load(write(`{"conversation_peek":false}`)); o.Peek || !o.Details {
		t.Fatalf("peek off: %+v", o)
	}
	if !Defaults().Peek {
		t.Fatal("the conversation peek should be on by default")
	}
	if _, err = Load(write(`{broken`)); err == nil {
		t.Fatal("accepted broken JSON")
	}
	// The shipped example must load as-is.
	if _, err = Load("../../config.json.example"); err != nil {
		t.Fatalf("config.json.example: %v", err)
	}
}

func TestSaveRoundTripKeepsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"future_option": 7, "mouse": true, "auto_delete_headless": "10m"}`), 0o600)
	want := Options{Mouse: false, ColorTabs: true, Details: false, Peek: false}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"future_option": 7`) || strings.Contains(string(b), "auto_delete_headless") {
		t.Fatalf("file:\n%s", b)
	}
}

func TestSaveOverNull(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte("null"), 0o600)
	if err := Save(p, Defaults()); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got != Defaults() {
		t.Fatalf("%+v %v", got, err)
	}
}
