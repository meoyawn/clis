package cli

import (
	"context"
	"net/http"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
	"github.com/meoyawn/clis/ozon/internal/webview"
)

// webviewAntibot is the slow path, used only after Ozon rejects abt_data: the
// system web engine loads ozon.ru and runs Ozon's own anti-bot script there.
// The fast path never starts the web view.
func webviewAntibot(ctx context.Context) ([]*http.Cookie, error) {
	ctx, cancel := context.WithTimeout(ctx, webview.Timeout)
	defer cancel()
	return webview.Cookies(ctx, ozon.WebOrigin+"/", ozon.AntibotCookie)
}
