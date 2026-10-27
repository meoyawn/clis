package service

import (
	"bytes"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"html"
	"strings"
)

func document(body []byte) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(body))
}
func stateNode(doc *goquery.Document, id string) ([]byte, error) {
	n := doc.Find("#" + id).First()
	if n.Length() == 0 {
		return nil, fmt.Errorf("catalog data node %q is missing", id)
	}
	var s strings.Builder
	for node := n.Nodes[0].FirstChild; node != nil; node = node.NextSibling {
		s.WriteString(node.Data)
	}
	v := strings.TrimSpace(s.String())
	v = strings.TrimPrefix(v, "<!--")
	v = strings.TrimSuffix(v, "-->")
	return []byte(strings.TrimSpace(v)), nil
}

func clean(value string) string {
	d, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + value + "</body>"))
	if err == nil {
		value = d.Find("body").Text()
	}
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}
