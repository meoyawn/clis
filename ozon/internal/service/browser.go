package service

import (
	"fmt"
)

type Browser struct{ value string }

const (
	chromeBrowserValue  = "chrome"
	firefoxBrowserValue = "firefox"
	braveBrowserValue   = "brave"
	edgeBrowserValue    = "edge"
	safariBrowserValue  = "safari"
)

var allowedBrowserValues = [...]string{chromeBrowserValue, firefoxBrowserValue, braveBrowserValue, edgeBrowserValue, safariBrowserValue}

func ChromeBrowser() Browser  { return Browser{value: chromeBrowserValue} }
func FirefoxBrowser() Browser { return Browser{value: firefoxBrowserValue} }
func BraveBrowser() Browser   { return Browser{value: braveBrowserValue} }
func EdgeBrowser() Browser    { return Browser{value: edgeBrowserValue} }
func SafariBrowser() Browser  { return Browser{value: safariBrowserValue} }

func NewBrowserFromValue(value string) (Browser, error) {
	switch value {
	case chromeBrowserValue:
		return ChromeBrowser(), nil
	case firefoxBrowserValue:
		return FirefoxBrowser(), nil
	case braveBrowserValue:
		return BraveBrowser(), nil
	case edgeBrowserValue:
		return EdgeBrowser(), nil
	case safariBrowserValue:
		return SafariBrowser(), nil
	default:
		return Browser{}, fmt.Errorf("invalid browser %q; allowed values: %v", value, allowedBrowserValues)
	}
}

func (b Browser) String() string { return b.value }
