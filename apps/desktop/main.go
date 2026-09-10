// Command zchat is the ZChat desktop client.
package main

import (
	"context"
	"os"

	_ "embed"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/zealish/zchat/packages/shared/logging"
	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

const appID = "com.zealish.ZChat"

//go:embed ui/zchat.gresource
var gresourceData []byte

func main() {
	log := logging.Setup("desktop", os.Getenv("ZCHAT_DEBUG") != "")

	resource, err := gio.NewResourceFromData(glib.NewBytes(gresourceData))
	if err != nil {
		log.Fatal().Err(err).Msg("load embedded resources")
	}
	gio.ResourcesRegister(resource)

	app := adw.NewApplication(appID, gio.ApplicationFlagsNone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app.ConnectActivate(func() {
		loadCSS()
		w := newWindow(ctx, app, log, xdgpaths.SocketPath())
		w.present()
	})

	os.Exit(app.Run(os.Args))
}

func loadCSS() {
	provider := gtk.NewCSSProvider()
	provider.LoadFromResource("/com/zealish/ZChat/style.css")
	if display := gdk.DisplayGetDefault(); display != nil {
		gtk.StyleContextAddProviderForDisplay(display, provider, uint(gtk.STYLE_PROVIDER_PRIORITY_APPLICATION))
	}
}
