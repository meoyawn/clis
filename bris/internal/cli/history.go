package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func printReportedUsage(out io.Writer, p page, plan []plannedReading) {
	rows := find(p.doc, func(n *html.Node) bool { return n.Data == "tr" })
	for _, item := range plan {
		var usages []string
		for _, row := range rows {
			fields := map[string]string{}
			for _, cell := range find(row, func(n *html.Node) bool { return n.Data == "td" && ancestor(n, "tr") == row }) {
				fields[attr(cell, "data-title")] = textContent(cell)
			}
			if fields["Номер счетчика"] != item.Counter.Number || fields["Расход"] == "" {
				continue
			}
			parts := strings.Fields(fields["Показания"])
			if len(parts) == 0 {
				continue
			}
			zone := 1
			switch parts[0] {
			case "день":
				zone, parts = 2, parts[1:]
			case "ночь":
				zone, parts = 3, parts[1:]
			}
			value, valueErr := strconv.ParseInt(strings.Join(parts, ""), 10, 64)
			date, dateErr := parseRussianDate(fields["Дата занесения"], item.Counter.Date.Location())
			if valueErr == nil && dateErr == nil && zone == item.Counter.Zone && value == item.Counter.Previous && date.Equal(item.Counter.Date) {
				usages = append(usages, fields["Расход"])
			}
		}
		if len(usages) == 1 {
			fmt.Fprintf(out, "BRIS reported usage: %s %s %s (reading %d dated %s)\n", item.Label, usages[0], item.Unit, item.Counter.Previous, item.Counter.Date.Format(time.DateOnly))
		} else {
			fmt.Fprintf(out, "BRIS reported usage: %s unavailable (%d matching history rows for reading %d dated %s)\n", item.Label, len(usages), item.Counter.Previous, item.Counter.Date.Format(time.DateOnly))
		}
	}
}
