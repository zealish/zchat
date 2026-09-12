package main

import (
	"testing"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// Opening a chat clears its unread badge, which comes back as a ChatUpdated
// event. Merging it into the pointer the list model already holds is what keeps
// the row alive; swapping in the new pointer would splice the row away and
// throw the sidebar back to the top.
func TestCopyChatInto(t *testing.T) {
	dst := &zchatv1.Chat{
		Jid:         "62812@s.whatsapp.net",
		Name:        "Old",
		Unread:      4,
		LastMessage: "old",
		UpdatedAt:   100,
	}
	src := &zchatv1.Chat{
		Jid:         "62812@s.whatsapp.net",
		Name:        "New",
		Unread:      0,
		Archived:    true,
		Pinned:      true,
		LastMessage: "new",
		UpdatedAt:   200,
		IsGroup:     true,
		MutedUntil:  -1,
	}

	copyChatInto(dst, src)

	if dst.GetName() != "New" || dst.GetUnread() != 0 || dst.GetLastMessage() != "new" {
		t.Errorf("display fields not merged: %+v", dst)
	}
	if !dst.GetArchived() || !dst.GetPinned() || !dst.GetIsGroup() || dst.GetMutedUntil() != -1 {
		t.Errorf("flags not merged: %+v", dst)
	}
	if dst.GetUpdatedAt() != 200 {
		t.Errorf("got updated_at %d, want 200", dst.GetUpdatedAt())
	}
	if dst == src {
		t.Fatal("dst must stay the pointer the list model holds")
	}
}

// The sidebar splices in place so a chat that only changed its badge does not
// destroy every row and reset the scroll position, so the computed range has to
// be both minimal and correct.
func TestSpliceRange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  []string
		want     []string
		position int
		removals int
		changed  bool
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}, 0, 0, false},
		{"both empty", nil, nil, 0, 0, false},
		{"initial fill", nil, []string{"a", "b"}, 0, 0, true},
		{"cleared", []string{"a", "b"}, nil, 0, 2, true},
		{"middle replaced", []string{"a", "b", "c"}, []string{"a", "x", "c"}, 1, 1, true},
		{"head replaced", []string{"a", "b", "c"}, []string{"x", "b", "c"}, 0, 1, true},
		{"tail replaced", []string{"a", "b", "c"}, []string{"a", "b", "x"}, 2, 1, true},
		{"inserted", []string{"a", "c"}, []string{"a", "b", "c"}, 1, 0, true},
		{"removed", []string{"a", "b", "c"}, []string{"a", "c"}, 1, 1, true},
		{"moved to front", []string{"a", "b", "c"}, []string{"c", "a", "b"}, 0, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position, removals, changed := spliceRange(tc.current, tc.want)
			if changed != tc.changed {
				t.Fatalf("got changed %v, want %v", changed, tc.changed)
			}
			if !changed {
				return
			}
			if position != tc.position || removals != tc.removals {
				t.Errorf("got splice(%d, %d), want splice(%d, %d)", position, removals, tc.position, tc.removals)
			}

			// Applying the range must actually produce want.
			tailKept := len(tc.current) - position - removals
			inserted := tc.want[position : len(tc.want)-tailKept]
			got := append([]string{}, tc.current[:position]...)
			got = append(got, inserted...)
			got = append(got, tc.current[position+removals:]...)
			if len(got) != len(tc.want) {
				t.Fatalf("applied splice gives %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("applied splice gives %v, want %v", got, tc.want)
				}
			}
		})
	}
}
