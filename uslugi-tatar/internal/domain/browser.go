package domain

import "fmt"

type Browser struct{ value string }

const (
	chromeBrowserValue    = "chrome"
	chromiumBrowserValue  = "chromium"
	edgeBrowserValue      = "edge"
	braveBrowserValue     = "brave"
	vivaldiBrowserValue   = "vivaldi"
	operaBrowserValue     = "opera"
	arcBrowserValue       = "arc"
	heliumBrowserValue    = "helium"
	diaBrowserValue       = "dia"
	cometBrowserValue     = "comet"
	atlasBrowserValue     = "atlas"
	whaleBrowserValue     = "whale"
	firefoxBrowserValue   = "firefox"
	zenBrowserValue       = "zen"
	floorpBrowserValue    = "floorp"
	waterfoxBrowserValue  = "waterfox"
	librewolfBrowserValue = "librewolf"
	safariBrowserValue    = "safari"
)

var allowedBrowserValues = [...]string{chromeBrowserValue, chromiumBrowserValue, edgeBrowserValue, braveBrowserValue, vivaldiBrowserValue, operaBrowserValue, arcBrowserValue, heliumBrowserValue, diaBrowserValue, cometBrowserValue, atlasBrowserValue, whaleBrowserValue, firefoxBrowserValue, zenBrowserValue, floorpBrowserValue, waterfoxBrowserValue, librewolfBrowserValue, safariBrowserValue}

func ChromeBrowser() Browser    { return Browser{chromeBrowserValue} }
func ChromiumBrowser() Browser  { return Browser{chromiumBrowserValue} }
func EdgeBrowser() Browser      { return Browser{edgeBrowserValue} }
func BraveBrowser() Browser     { return Browser{braveBrowserValue} }
func VivaldiBrowser() Browser   { return Browser{vivaldiBrowserValue} }
func OperaBrowser() Browser     { return Browser{operaBrowserValue} }
func ArcBrowser() Browser       { return Browser{arcBrowserValue} }
func HeliumBrowser() Browser    { return Browser{heliumBrowserValue} }
func DiaBrowser() Browser       { return Browser{diaBrowserValue} }
func CometBrowser() Browser     { return Browser{cometBrowserValue} }
func AtlasBrowser() Browser     { return Browser{atlasBrowserValue} }
func WhaleBrowser() Browser     { return Browser{whaleBrowserValue} }
func FirefoxBrowser() Browser   { return Browser{firefoxBrowserValue} }
func ZenBrowser() Browser       { return Browser{zenBrowserValue} }
func FloorpBrowser() Browser    { return Browser{floorpBrowserValue} }
func WaterfoxBrowser() Browser  { return Browser{waterfoxBrowserValue} }
func LibreWolfBrowser() Browser { return Browser{librewolfBrowserValue} }
func SafariBrowser() Browser    { return Browser{safariBrowserValue} }

func NewBrowserFromValue(value string) (Browser, error) {
	switch value {
	case chromeBrowserValue:
		return ChromeBrowser(), nil
	case chromiumBrowserValue:
		return ChromiumBrowser(), nil
	case edgeBrowserValue:
		return EdgeBrowser(), nil
	case braveBrowserValue:
		return BraveBrowser(), nil
	case vivaldiBrowserValue:
		return VivaldiBrowser(), nil
	case operaBrowserValue:
		return OperaBrowser(), nil
	case arcBrowserValue:
		return ArcBrowser(), nil
	case heliumBrowserValue:
		return HeliumBrowser(), nil
	case diaBrowserValue:
		return DiaBrowser(), nil
	case cometBrowserValue:
		return CometBrowser(), nil
	case atlasBrowserValue:
		return AtlasBrowser(), nil
	case whaleBrowserValue:
		return WhaleBrowser(), nil
	case firefoxBrowserValue:
		return FirefoxBrowser(), nil
	case zenBrowserValue:
		return ZenBrowser(), nil
	case floorpBrowserValue:
		return FloorpBrowser(), nil
	case waterfoxBrowserValue:
		return WaterfoxBrowser(), nil
	case librewolfBrowserValue:
		return LibreWolfBrowser(), nil
	case safariBrowserValue:
		return SafariBrowser(), nil
	}
	return Browser{}, fmt.Errorf("invalid browser %q; allowed values: %v", value, allowedBrowserValues)
}

func (browser Browser) String() string { return browser.value }
