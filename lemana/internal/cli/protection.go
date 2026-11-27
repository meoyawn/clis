package cli

import (
	"context"
	"net/http"

	"github.com/meoyawn/clis/lemana/internal/service"
	"github.com/meoyawn/clis/lemana/internal/webview"
)

// webviewProtection is the slow path, used only when the saved protection
// cookie is stale or rejected: the system web engine loads the storefront and
// runs the protection service's own check there.
func webviewProtection(ctx context.Context) ([]*http.Cookie, error) {
	ctx, cancel := context.WithTimeout(ctx, webview.Timeout)
	defer cancel()
	return webview.Cookies(ctx, service.Origin+"/", service.ProtectionCookie)
}
