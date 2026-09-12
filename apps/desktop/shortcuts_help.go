package main

import "github.com/diamondburned/gotk4-adwaita/pkg/adw"

// showShortcutsHelp lists the window's keyboard shortcuts. The accelerators are
// spelled the same way setupShortcuts binds them.
func (w *window) showShortcutsHelp() {
	dialog := adw.NewShortcutsDialog()

	sections := []struct {
		title string
		items [][2]string
	}{
		{"Chats", [][2]string{
			{"Search chats", "<Control>k"},
			{"New chat", "<Control>n"},
			{"Focus chat list", "<Control>l"},
			{"Mute chat", "<Control><Shift>m"},
		}},
		{"Conversation", [][2]string{
			{"Find in conversation", "<Control>f"},
			{"Paste image", "<Control>v"},
			{"Send message", "Return"},
		}},
		{"Window", [][2]string{
			{"Keyboard shortcuts", "<Control>question"},
			{"Close window", "<Control>w"},
			{"Quit", "<Control>q"},
			{"Cancel", "Escape"},
		}},
	}

	for _, section := range sections {
		s := adw.NewShortcutsSection(section.title)
		for _, item := range section.items {
			s.Add(adw.NewShortcutsItem(item[0], item[1]))
		}
		dialog.Add(s)
	}

	dialog.Present(w.win)
}
