package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if o, _ = Load(write(`{"auto_delete_headless":"off","mouse":false}`)); o.AutoDelete != 0 || o.Mouse || !o.Details {
		t.Fatalf("off: %+v", o)
	}
	if o, _ = Load(write(`{"auto_delete_headless":"90s"}`)); o.AutoDelete != 90*time.Second {
		t.Fatalf("90s: %+v", o)
	}
	if _, err = Load(write(`{"auto_delete_headless":"soon"}`)); err == nil {
		t.Fatal("accepted a bad duration")
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
	os.WriteFile(p, []byte(`{"future_option": 7, "mouse": true}`), 0o600)
	want := Options{Mouse: false, ColorTabs: true, Details: false, AutoDelete: 90 * time.Minute}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"future_option": 7`) || !strings.Contains(string(b), `"1h30m"`) {
		t.Fatalf("file:\n%s", b)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "off", 10 * time.Minute: "10m", time.Hour: "1h", 90 * time.Second: "1m30s", 45 * time.Second: "45s", 90 * time.Minute: "1h30m"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: got %q want %q", d, got, want)
		}
		if back, err := ParseDuration(FormatDuration(d)); err != nil || back != d {
			t.Errorf("%v: round trip %v %v", d, back, err)
		}
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
