package service

import (
	"bytes"
	"html"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func document(body []byte) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(body))
}

// clean collapses whitespace and strips markup from a text fragment.
func clean(value string) string {
	d, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + value + "</body>"))
	if err == nil {
		value = d.Find("body").Text()
	}
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}
