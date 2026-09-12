package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

// settingsFile is the desktop client's preferences, stored under
// ~/.config/zchat/. The daemon has no say in these, so they never cross the
// socket.
const settingsFile = "settings.json"

// theme selects the Libadwaita colour scheme. The zero value follows the system.
type theme string

const (
	themeSystem theme = "system"
	themeLight  theme = "light"
	themeDark   theme = "dark"
)

// themeOrder is the order the preference dialog lists themes in.
var themeOrder = []theme{themeSystem, themeLight, themeDark}

type settings struct {
	Theme         theme `json:"theme"`
	Notifications bool  `json:"notifications"`
}

func defaultSettings() settings {
	return settings{Theme: themeSystem, Notifications: true}
}

func settingsPath() (string, error) {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFile), nil
}

// loadSettings reads the preferences file. A missing or unreadable file is not
// an error: the defaults apply and the file is written on the next change.
func loadSettings() settings {
	out := defaultSettings()
	path, err := settingsPath()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return defaultSettings()
	}
	if out.Theme != themeLight && out.Theme != themeDark {
		out.Theme = themeSystem
	}
	return out
}

func (s settings) save() error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// colorScheme maps a stored theme onto the Libadwaita scheme. "system" defers
// to the desktop's own light/dark preference.
func (t theme) colorScheme() adw.ColorScheme {
	switch t {
	case themeLight:
		return adw.ColorSchemeForceLight
	case themeDark:
		return adw.ColorSchemeForceDark
	default:
		return adw.ColorSchemeDefault
	}
}

func (t theme) label() string {
	switch t {
	case themeLight:
		return "Light"
	case themeDark:
		return "Dark"
	default:
		return "Follow system"
	}
}

// applyTheme pushes the stored colour scheme onto the running application.
func (w *window) applyTheme() {
	adw.StyleManagerGetDefault().SetColorScheme(w.settings.Theme.colorScheme())
}

// showSettings presents the preferences dialog: appearance, notifications,
// device state, storage locations, and logout.
func (w *window) showSettings() {
	dialog := adw.NewPreferencesDialog()
	dialog.SetTitle("Preferences")

	page := adw.NewPreferencesPage()
	page.SetTitle("General")
	page.SetIconName("preferences-system-symbolic")
	page.Add(w.appearanceGroup())
	page.Add(w.notificationGroup())
	page.Add(w.deviceGroup())
	page.Add(w.storageGroup())
	page.Add(w.accountGroup(dialog))
	dialog.Add(page)
	dialog.Present(w.win)
}

func (w *window) appearanceGroup() *adw.PreferencesGroup {
	group := adw.NewPreferencesGroup()
	group.SetTitle("Appearance")

	labels := make([]string, 0, len(themeOrder))
	selected := uint(0)
	for i, t := range themeOrder {
		labels = append(labels, t.label())
		if t == w.settings.Theme {
			selected = uint(i)
		}
	}

	row := adw.NewComboRow()
	row.SetTitle("Theme")
	row.SetSubtitle("Light and dark styling for the whole window")
	row.SetModel(gtk.NewStringList(labels))
	row.SetSelected(selected)
	row.NotifyProperty("selected", func() {
		index := int(row.Selected())
		if index < 0 || index >= len(themeOrder) {
			return
		}
		w.settings.Theme = themeOrder[index]
		w.applyTheme()
		w.persistSettings()
	})
	group.Add(row)
	return group
}

func (w *window) notificationGroup() *adw.PreferencesGroup {
	group := adw.NewPreferencesGroup()
	group.SetTitle("Notifications")

	row := adw.NewSwitchRow()
	row.SetTitle("Desktop notifications")
	row.SetSubtitle("Show a banner for incoming messages")
	row.SetActive(w.settings.Notifications)
	row.NotifyProperty("active", func() {
		w.settings.Notifications = row.Active()
		w.persistSettings()
	})
	group.Add(row)
	return group
}

func (w *window) deviceGroup() *adw.PreferencesGroup {
	group := adw.NewPreferencesGroup()
	group.SetTitle("Device")

	jid := w.ownJID
	if jid == "" {
		jid = "Not linked"
	}
	status := w.connStatus
	if status == "" {
		status = "UNKNOWN"
	}
	for _, row := range []struct{ title, value string }{
		{"Linked device", jid},
		{"Connection", status},
		{"Socket", w.sock},
	} {
		group.Add(infoRow(row.title, row.value))
	}
	return group
}

func (w *window) storageGroup() *adw.PreferencesGroup {
	group := adw.NewPreferencesGroup()
	group.SetTitle("Storage")

	dataDir, _ := xdgpaths.DataDir()
	cacheDir, _ := xdgpaths.CacheDir()
	mediaDir, _ := xdgpaths.MediaDir()
	for _, row := range []struct{ title, value string }{
		{"Data", dataDir},
		{"Cache", cacheDir},
		{"Media", mediaDir},
	} {
		group.Add(infoRow(row.title, row.value))
	}
	return group
}

func (w *window) accountGroup(dialog *adw.PreferencesDialog) *adw.PreferencesGroup {
	group := adw.NewPreferencesGroup()
	group.SetTitle("Account")

	row := adw.NewButtonRow()
	row.SetTitle("Log out")
	row.AddCSSClass("destructive-action")
	row.ConnectActivated(func() {
		dialog.Close()
		w.confirmLogout()
	})
	group.Add(row)
	return group
}

// infoRow is a read-only row whose value can be copied out of the dialog.
func infoRow(title, value string) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(title)
	row.SetSubtitle(value)
	row.SetSubtitleSelectable(true)
	return row
}

func (w *window) persistSettings() {
	if err := w.settings.save(); err != nil {
		w.log.Error().Err(err).Msg("save settings")
		w.toast("Could not save preferences")
	}
}

// confirmLogout guards the unlink, which cannot be undone without scanning a
// new QR code.
func (w *window) confirmLogout() {
	dialog := adw.NewAlertDialog("Log out?", "This device will be unlinked and you will have to scan the QR code again.")
	dialog.AddResponse("cancel", "Cancel")
	dialog.AddResponse("logout", "Log out")
	dialog.SetResponseAppearance("logout", adw.ResponseDestructive)
	dialog.SetDefaultResponse("cancel")
	dialog.SetCloseResponse("cancel")
	dialog.ConnectResponse(func(response string) {
		if response == "logout" {
			w.logout()
		}
	})
	dialog.Present(w.win)
}
