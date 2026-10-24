package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/uslugi-tatar/internal/domain"
	"golang.org/x/net/html"
)

var mainValueName = regexp.MustCompile(`^value\[([0-9]+)\]$`)
var meterDecimal = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,3})?$`)

type mainCounter struct {
	Name, Serial      string
	Service           domain.MainService
	Digits            int
	Previous, Current int64 // Thousandths, matching the portal's three decimal places.
}

type mainForm struct {
	form     form
	Counters []mainCounter
}

type mainTarget struct {
	Label, Unit string
	Service     domain.MainService
	Value       int64
}

func mainTargets(r mainReading) []mainTarget {
	return []mainTarget{
		{Label: "electricity", Service: domain.Electricity(), Unit: "kWh", Value: r.Electricity},
		{Label: "hot", Service: domain.HotWater(), Unit: "m³", Value: r.Hot},
		{Label: "cold", Service: domain.ColdWater(), Unit: "m³", Value: r.Cold},
	}
}

func verifyMainAccount(p page, r mainAccount) error {
	infos := find(p.doc, func(n *html.Node) bool { return hasClass(n, "account_info") })
	if len(infos) != 1 {
		return fmt.Errorf("main account confirmation is missing or ambiguous")
	}
	text := textContent(infos[0])
	account := regexp.MustCompile(`Лицевой счет:\s*([0-9]+)\b`).FindStringSubmatch(text)
	flat := regexp.MustCompile(`квар\.\s*([0-9]+)\b`).FindStringSubmatch(text)
	if len(account) != 2 || account[1] != r.Account {
		return fmt.Errorf("portal opened a different main account; expected %s", r.Account)
	}
	if len(flat) != 2 || flat[1] != r.Flat || !strings.Contains(text, r.Address) {
		return fmt.Errorf("main account %s does not match expected address %q, apartment %s", r.Account, r.Address, r.Flat)
	}
	return nil
}

func (c *portalClient) mainPage(ctx context.Context, r mainAccount) (page, error) {
	init := c.base.ResolveReference(&url.URL{Path: "/personal-account/init-by-user-tsj", RawQuery: url.Values{"informer_id": {r.Informer}, "informer_type": {"jkh_payment"}}.Encode()})
	start, err := c.request(ctx, init, nil)
	if err != nil {
		return page{}, err
	}
	choice, err := c.link(start, "/personal-account/choise")
	if err != nil {
		return page{}, err
	}
	services, err := c.request(ctx, choice, nil)
	if err != nil {
		return page{}, err
	}
	if err := verifyMainAccount(services, r); err != nil {
		return page{}, err
	}
	f, err := c.readForm(services, "serviceType", "/personal_account/choise")
	if err != nil {
		return page{}, err
	}
	f.fields.Set("serviceType", "counter/value?update")
	p, err := c.request(ctx, f.action, f.fields)
	if err != nil {
		return page{}, fmt.Errorf("open main meter service: %w", err)
	}
	if p.url.Path != "/personal-account/counter/value" {
		return page{}, fmt.Errorf("portal did not open main meter readings")
	}
	return p, nil
}

func parseMeterDecimal(value string) (int64, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), ",", ".")
	if !meterDecimal.MatchString(value) {
		return 0, fmt.Errorf("invalid portal meter value %q; expected at most three decimal places", value)
	}
	parts := strings.SplitN(value, ".", 2)
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	result, err := strconv.ParseInt(parts[0]+fraction+strings.Repeat("0", 3-len(fraction)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("portal meter value %q is out of range", value)
	}
	return result, nil
}

func (c *portalClient) parseMain(p page, r mainAccount) (mainForm, error) {
	if err := verifyMainAccount(p, r); err != nil {
		return mainForm{}, err
	}
	inputs := find(p.doc, func(n *html.Node) bool { return n.Data == "input" && mainValueName.MatchString(attr(n, "name")) })
	if len(inputs) != 3 {
		return mainForm{}, fmt.Errorf("expected three main meter inputs, found %d; the entry window may be closed", len(inputs))
	}
	f, err := c.readForm(p, attr(inputs[0], "name"), "/personal_account/counter/value")
	if err != nil {
		return mainForm{}, err
	}
	if attr(f.node, "id") != "value_form" {
		return mainForm{}, fmt.Errorf("main meter form ID has changed")
	}
	result := mainForm{form: f}
	seen := map[string]bool{}
	for _, input := range inputs {
		name := attr(input, "name")
		id := mainValueName.FindStringSubmatch(name)[1]
		if seen[name] {
			return mainForm{}, fmt.Errorf("duplicate main meter input %s", name)
		}
		seen[name] = true
		if _, disabled := attribute(input, "disabled"); disabled {
			return mainForm{}, fmt.Errorf("main meter %s is disabled for entry", id)
		}
		var row *html.Node
		for parent := input.Parent; parent != nil && parent != f.node; parent = parent.Parent {
			if parent.Data == "tr" {
				row = parent
				break
			}
		}
		if row == nil {
			return mainForm{}, fmt.Errorf("main meter %s has no service row", id)
		}
		var cells []*html.Node
		for child := row.FirstChild; child != nil; child = child.NextSibling {
			if child.Data == "td" {
				cells = append(cells, child)
			}
		}
		services := find(row, func(n *html.Node) bool { return hasClass(n, "nameService") })
		if len(cells) < 4 || len(services) != 1 {
			return mainForm{}, fmt.Errorf("main meter %s has incomplete service metadata", id)
		}
		digits, err := strconv.Atoi(textContent(cells[3]))
		if err != nil || digits < 1 || digits > 12 {
			return mainForm{}, fmt.Errorf("main meter %s has unsupported digit capacity %q", id, textContent(cells[3]))
		}
		for _, key := range []string{"old[" + id + "]", "item[" + id + "]", "check[" + id + "]"} {
			if len(f.fields[key]) != 1 {
				return mainForm{}, fmt.Errorf("main meter %s is missing unique hidden field %s", id, key)
			}
		}
		if f.fields.Get("item["+id+"]") == "" {
			return mainForm{}, fmt.Errorf("main meter %s has empty serialized metadata", id)
		}
		previous, err := parseMeterDecimal(f.fields.Get("old[" + id + "]"))
		if err != nil {
			return mainForm{}, err
		}
		current := int64(0)
		if value := attr(input, "value"); value != "" {
			current, err = parseMeterDecimal(value)
			if err != nil {
				return mainForm{}, err
			}
		}
		service, err := domain.NewMainServiceFromValue(textContent(services[0]))
		if err != nil {
			return mainForm{}, err
		}
		result.Counters = append(result.Counters, mainCounter{Name: name, Service: service, Serial: textContent(cells[1]), Digits: digits, Previous: previous, Current: current})
	}
	return result, nil
}

func matchingMainCounter(f mainForm, service domain.MainService) (mainCounter, error) {
	var matches []mainCounter
	for _, counter := range f.Counters {
		if counter.Service == service {
			matches = append(matches, counter)
		}
	}
	if len(matches) != 1 {
		return mainCounter{}, fmt.Errorf("main service %q: expected one meter, found %d", service, len(matches))
	}
	if matches[0].Serial == "" {
		return mainCounter{}, fmt.Errorf("main service %q has no meter serial", service)
	}
	return matches[0], nil
}

func (c *portalClient) submitMain(ctx context.Context, r mainReading, dryRun bool, out io.Writer) error {
	p, err := c.mainPage(ctx, r.Routing)
	if err != nil {
		return err
	}
	snapshot, err := c.parseMain(p, r.Routing)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Main account %s; %s, apartment %s; reading date %s\n", r.Routing.Account, r.Routing.Address, r.Routing.Flat, r.Date.Format(time.DateOnly))
	printMainHistory(out, r)
	changed := 0
	for _, target := range mainTargets(r) {
		counter, err := matchingMainCounter(snapshot, target.Service)
		if err != nil {
			return err
		}
		if target.Value < 0 || len(strconv.FormatInt(target.Value, 10)) > counter.Digits {
			return fmt.Errorf("%s reading %d exceeds meter capacity %d or is negative", target.Label, target.Value, counter.Digits)
		}
		value := target.Value * 1000
		if value < counter.Previous || value < counter.Current {
			return fmt.Errorf("%s reading %d is below portal previous/current values %s/%s", target.Label, target.Value, formatMeterDecimal(counter.Previous), formatMeterDecimal(counter.Current))
		}
		if value != counter.Current {
			changed++
		}
		snapshot.form.fields.Set(counter.Name, formatMeterDecimal(value))
		fmt.Fprintf(out, "%s (meter %s): previous %s; current %s -> %s; usage %s %s\n", target.Label, counter.Serial, formatMeterDecimal(counter.Previous), formatMeterDecimal(counter.Current), formatMeterDecimal(value), formatMeterDecimal(value-counter.Previous), target.Unit)
	}
	if dryRun {
		fmt.Fprintf(out, "Dry run: %d changed main readings; no electricity or water readings submitted.\n", changed)
		return nil
	}
	if changed == 0 {
		fmt.Fprintln(out, "All 3 main readings already match the portal; nothing submitted.")
		return nil
	}
	// Preserve the portal's fields, including serialized PHP items
	// and empty check[] values, exactly as the browser's form submission does.
	_, err = c.request(ctx, snapshot.form.action, snapshot.form.fields)
	if err != nil {
		return fmt.Errorf("main submission outcome is unknown; not retrying automatically: %w; inspect with --dry-run", err)
	}
	saved, err := c.request(ctx, p.url, nil)
	if err != nil {
		return fmt.Errorf("main submitted, but read-back failed: %w", err)
	}
	verified, err := c.parseMain(saved, r.Routing)
	if err != nil {
		return fmt.Errorf("main read-back: %w", err)
	}
	for _, target := range mainTargets(r) {
		before, _ := matchingMainCounter(snapshot, target.Service)
		after, err := matchingMainCounter(verified, target.Service)
		if err != nil {
			return fmt.Errorf("main read-back: %w", err)
		}
		if after.Serial != before.Serial || after.Current != target.Value*1000 {
			return fmt.Errorf("main read-back mismatch for %s: expected meter %s value %d, got meter %s value %s", target.Label, before.Serial, target.Value, after.Serial, formatMeterDecimal(after.Current))
		}
	}
	fmt.Fprintln(out, "Electricity and water submitted and verified for all 3 main meters.")
	return nil
}

func formatMeterDecimal(value int64) string { return fmt.Sprintf("%d.%03d", value/1000, value%1000) }
