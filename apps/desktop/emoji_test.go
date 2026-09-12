package main

import "testing"

func TestSpliceRunes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		text     string
		position int
		want     string
		caret    int
	}{
		{"empty entry", "", "😀", 0, "😀", 1},
		{"append", "hi", "😀", 2, "hi😀", 3},
		{"middle", "hi there", "😀", 2, "hi😀 there", 3},
		{"after multibyte", "héllo", "😀", 2, "hé😀llo", 3},
		{"position past end", "hi", "😀", 99, "hi😀", 3},
		{"negative position", "hi", "😀", -1, "hi😀", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, caret := spliceRunes(tc.body, tc.text, tc.position)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if caret != tc.caret {
				t.Errorf("got caret %d, want %d", caret, tc.caret)
			}
		})
	}
}
