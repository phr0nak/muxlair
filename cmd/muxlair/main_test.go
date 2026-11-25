package main

import "testing"

func TestFirstWords(t *testing.T) {
	for in, want := range map[string]string{
		"one two three":           "one two three",
		"a b c d e f":             "a b c d e f",
		"a b c d e f g h":         "a b c d e f…",
		"  spaced   out\twords  ": "spaced out words",
		"":                        "",
	} {
		if got := firstWords(in, 6); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
