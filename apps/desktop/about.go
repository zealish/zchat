package main

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const projectURL = "https://github.com/zealish/zchat"

func (w *window) showAbout() {
	dialog := adw.NewAboutDialog()
	dialog.SetApplicationName("ZChat")
	dialog.SetApplicationIcon(appID)
	dialog.SetVersion(version)
	dialog.SetDeveloperName("Zealish")
	dialog.SetComments("Native Linux desktop client for WhatsApp Multi-Device, powered by WhatsMeow.\n\n" +
		"ZChat is independent software and is not affiliated with, endorsed by, or sponsored by " +
		"WhatsApp LLC or Meta Platforms, Inc.")
	dialog.SetLicenseType(gtk.LicenseGPL30)
	dialog.SetWebsite(projectURL)
	dialog.SetIssueURL(projectURL + "/issues")
	dialog.Present(w.win)
}
