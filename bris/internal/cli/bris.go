package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

const counterPath = "/MILLENIUM2/Home/Counter"

var valueName = regexp.MustCompile(`^counters\[([0-9]+)\]\.Value$`)

type brisClient struct {
	base  *url.URL
	http  *http.Client
	creds credentials
}

type page struct {
	url *url.URL
	doc *html.Node
}

type counter struct {
	Number   string
	Zone     int
	Previous int64
	Date     time.Time
	Name     string
	Fields   url.Values
	Capacity int
}

type counterPage struct {
	Action   *url.URL
	Shared   url.Values
	Counters []counter
}

type plannedReading struct {
	Label   string
	Value   int64
	Counter counter
}

func newBrisClient(baseURL string, creds credentials) (*brisClient, error) {
	for _, field := range []struct{ name, value string }{
		{"login", creds.Login},
		{"password", creds.Password},
		{"account", creds.Account},
		{"meters.water", creds.Meters.Water},
		{"meters.day", creds.Meters.Day},
		{"meters.night", creds.Meters.Night},
		{"meters.drinking", creds.Meters.Drinking},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("credentials require nonempty %s", field.name)
		}
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil {
		return nil, fmt.Errorf("invalid BRIS base URL; expected an HTTP(S) origin without credentials")
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create session cookie jar: %w", err)
	}
	c := &brisClient{base: base, creds: creds}
	c.http = &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("BRIS exceeded 10 redirects")
			}
			return c.checkOrigin(req.URL)
		},
	}
	return c, nil
}

func (c *brisClient) checkOrigin(target *url.URL) error {
	if target.Scheme != c.base.Scheme || !strings.EqualFold(target.Host, c.base.Host) || target.User != nil {
		return fmt.Errorf("BRIS tried to leave its configured origin")
	}
	return nil
}

func (c *brisClient) request(ctx context.Context, target *url.URL, fields url.Values) (page, error) {
	if err := c.checkOrigin(target); err != nil {
		return page{}, err
	}
	method := http.MethodGet
	var body io.Reader
	if fields != nil {
		method, body = http.MethodPost, strings.NewReader(fields.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return page{}, fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("User-Agent", "bris-cli/1.0")
	req.Header.Set("Accept", "text/html")
	if fields != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", c.base.String())
		req.Header.Set("Referer", c.base.ResolveReference(&url.URL{Path: counterPath}).String())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return page{}, fmt.Errorf("%s %s: %w", method, target.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page{}, fmt.Errorf("%s %s returned HTTP %d", method, target.Path, resp.StatusCode)
	}
	const maxPage = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	if err != nil {
		return page{}, fmt.Errorf("read BRIS response: %w", err)
	}
	if len(data) > maxPage {
		return page{}, fmt.Errorf("BRIS response exceeds %d bytes", maxPage)
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return page{}, fmt.Errorf("parse BRIS HTML: %w", err)
	}
	return page{resp.Request.URL, doc}, nil
}

func (c *brisClient) login(ctx context.Context, p page) error {
	password := find(p.doc, func(n *html.Node) bool { return n.Data == "input" && attr(n, "name") == "Password" })
	if len(password) != 1 {
		return fmt.Errorf("BRIS login page has %d password fields; expected one", len(password))
	}
	form := ancestor(password[0], "form")
	if form == nil || !strings.EqualFold(attr(form, "method"), "post") {
		return fmt.Errorf("BRIS login form is missing or no longer uses POST")
	}
	action, err := p.url.Parse(attr(form, "action"))
	if err != nil {
		return fmt.Errorf("invalid BRIS login form action")
	}
	fields := hiddenFields(form)
	fields.Set("Login", c.creds.Login)
	fields.Set("Password", c.creds.Password)
	fields.Set("RememberMe", "true")
	result, err := c.request(ctx, action, fields)
	if err != nil {
		return fmt.Errorf("log in: %w", err)
	}
	if isLogin(result) {
		return fmt.Errorf("BRIS rejected login; check the saved login and password")
	}
	return nil
}

func isLogin(p page) bool {
	return len(find(p.doc, func(n *html.Node) bool { return n.Data == "input" && attr(n, "name") == "Password" })) > 0
}

func (c *brisClient) counters(ctx context.Context) (page, error) {
	target := c.base.ResolveReference(&url.URL{Path: counterPath})
	p, err := c.request(ctx, target, nil)
	if err != nil {
		return page{}, err
	}
	if isLogin(p) {
		if err := c.login(ctx, p); err != nil {
			return page{}, err
		}
		p, err = c.request(ctx, target, nil)
		if err != nil {
			return page{}, err
		}
		if isLogin(p) {
			return page{}, fmt.Errorf("BRIS session expired immediately after login")
		}
	}
	return p, nil
}

func parseCounters(p page, zone *time.Location, accountNumber string) (counterPage, error) {
	var result counterPage
	accounts := find(p.doc, func(n *html.Node) bool {
		return n.Data == "a" && attr(n, "title") == "Текущий лицевой счет"
	})
	if len(accounts) != 1 || textContent(accounts[0]) != accountNumber {
		return result, fmt.Errorf("BRIS current account does not match the configured account")
	}
	forms := find(p.doc, func(n *html.Node) bool { return n.Data == "form" && attr(n, "id") == "counterForm" })
	containers := find(p.doc, func(n *html.Node) bool { return hasClass(n, "iCounterTable") })
	if len(forms) != 1 || len(containers) != 1 || !strings.EqualFold(attr(forms[0], "method"), "post") {
		return result, fmt.Errorf("BRIS counter form is missing or ambiguous")
	}
	var err error
	result.Action, err = p.url.Parse(attr(forms[0], "action"))
	if err != nil || !strings.EqualFold(result.Action.Path, counterPath) {
		return result, fmt.Errorf("BRIS counter form action has changed")
	}
	// BRIS puts <form> inside <tbody>. HTML5 parsing detaches the form
	// from its controls, so read the counter table rather than form children.
	result.Shared = hiddenFields(forms[0])
	for name := range result.Shared {
		if strings.HasPrefix(name, "counters") {
			delete(result.Shared, name)
		}
	}
	for _, input := range find(containers[0], func(n *html.Node) bool { return n.Data == "input" && attr(n, "type") == "hidden" }) {
		name := attr(input, "name")
		if name != "" && !strings.HasPrefix(name, "counters") {
			result.Shared.Set(name, attr(input, "value"))
		}
	}
	var number string
	for _, row := range find(containers[0], func(n *html.Node) bool { return n.Data == "tr" }) {
		cells := find(row, func(n *html.Node) bool { return n.Data == "td" && ancestor(n, "tr") == row })
		var previous, dateText string
		for _, cell := range cells {
			switch attr(cell, "data-title") {
			case "Номер счетчика":
				number = textContent(cell)
			case "Предыдущее показание":
				previous = textContent(cell)
			case "Дата занесения":
				dateText = textContent(cell)
			}
		}
		for _, input := range find(row, func(n *html.Node) bool { return n.Data == "input" && valueName.MatchString(attr(n, "name")) }) {
			if _, disabled := attribute(input, "disabled"); disabled {
				return result, fmt.Errorf("meter %s is disabled for entry", number)
			}
			name := attr(input, "name")
			prefix := strings.TrimSuffix(name, "Value")
			fields := hiddenFields(row)
			zoneValue, zoneErr := strconv.Atoi(fields.Get(prefix + "Zone"))
			capacity, capErr := strconv.Atoi(fields.Get(prefix + "Capacity"))
			old, oldErr := strconv.ParseInt(strings.Join(strings.Fields(previous), ""), 10, 64)
			date, dateErr := parseRussianDate(dateText, zone)
			if number == "" || zoneErr != nil || capErr != nil || capacity < 1 || capacity > 18 || oldErr != nil || old < 0 || dateErr != nil || fields.Get(prefix+"ACounterId") == "" || fields.Get(prefix+"Coeff") == "" || fields.Get("counters.index") != valueName.FindStringSubmatch(name)[1] {
				return result, fmt.Errorf("invalid or incomplete BRIS metadata for meter %q field %s", number, name)
			}
			result.Counters = append(result.Counters, counter{number, zoneValue, old, date, name, fields, capacity})
		}
	}
	return result, nil
}

func planReadings(p counterPage, r reading, meters meterNumbers) ([]plannedReading, url.Values, error) {
	targets := []struct {
		label, number string
		zone          int
		value         int64
	}{
		{"water", meters.Water, 1, r.Water},
		{"day", meters.Day, 2, r.Day},
		{"night", meters.Night, 3, r.Night},
		{"drinking", meters.Drinking, 1, r.Drinking},
	}
	fields := url.Values{}
	for name, values := range p.Shared {
		fields[name] = append([]string(nil), values...)
	}
	var plan []plannedReading
	for _, target := range targets {
		var matches []counter
		for _, counter := range p.Counters {
			if counter.Number == target.number && counter.Zone == target.zone {
				matches = append(matches, counter)
			}
		}
		if len(matches) != 1 {
			return nil, nil, fmt.Errorf("%s: expected one meter %s tariff %d, found %d", target.label, target.number, target.zone, len(matches))
		}
		counter := matches[0]
		if target.value < counter.Previous {
			return nil, nil, fmt.Errorf("%s reading %d is below BRIS reading %d", target.label, target.value, counter.Previous)
		}
		if len(strconv.FormatInt(target.value, 10)) > counter.Capacity {
			return nil, nil, fmt.Errorf("%s reading %d exceeds meter capacity %d", target.label, target.value, counter.Capacity)
		}
		for name, values := range counter.Fields {
			for _, value := range values {
				fields.Add(name, value)
			}
		}
		fields.Set(counter.Name, strconv.FormatInt(target.value, 10))
		plan = append(plan, plannedReading{target.label, target.value, counter})
	}
	return plan, fields, nil
}

func (c *brisClient) submit(ctx context.Context, reading reading, now time.Time, dryRun bool, out io.Writer) error {
	p, err := c.counters(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Readings: %s; BRIS account %s\n", reading.Date.Format(time.DateOnly), c.creds.Account)
	printHistoricalUsage(out, reading, now)
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err := parseCounters(p, now.Location(), c.creds.Account)
		if err != nil {
			return err
		}
		plan, fields, err := planReadings(snapshot, reading, c.creds.Meters)
		if err != nil {
			return err
		}
		if attempt == 0 {
			for _, item := range plan {
				fmt.Fprintf(out, "%s: %d -> %d (usage %d)\n", item.Label, item.Counter.Previous, item.Value, item.Value-item.Counter.Previous)
			}
		}
		if dryRun {
			fmt.Fprintln(out, "Dry run: no meter readings submitted. Gas is handled separately on Госуслуги РТ.")
			return nil
		}
		response, err := c.request(ctx, snapshot.Action, fields)
		if err != nil {
			return fmt.Errorf("submission outcome is unknown; not retrying automatically: %w; use --dry-run to inspect saved readings", err)
		}
		if isLogin(response) {
			if attempt == 1 {
				return fmt.Errorf("BRIS rejected the submission because the session expired twice")
			}
			fmt.Fprintln(out, "Session expired before submission; logging in again and refreshing meter metadata.")
			p, err = c.counters(ctx)
			if err != nil {
				return err
			}
			continue
		}
		accepted := find(response.doc, func(n *html.Node) bool {
			return hasClass(n, "alert-success") && strings.Contains(textContent(n), "Введенные показания счетчиков приняты")
		})
		if len(accepted) == 0 {
			return fmt.Errorf("BRIS did not confirm acceptance; not retrying automatically; use --dry-run to inspect saved readings")
		}
		// A success banner alone does not prove that values were stored.
		saved, err := c.counters(ctx)
		if err != nil {
			return fmt.Errorf("BRIS accepted submission, but read-back verification failed: %w", err)
		}
		verified, err := parseCounters(saved, now.Location(), c.creds.Account)
		if err != nil {
			return fmt.Errorf("read-back verification: %w", err)
		}
		checked, _, err := planReadings(verified, reading, c.creds.Meters)
		if err != nil {
			return fmt.Errorf("read-back verification: %w", err)
		}
		for _, item := range checked {
			if item.Counter.Previous != item.Value || item.Counter.Date.Year() != now.Year() || item.Counter.Date.Month() != now.Month() {
				return fmt.Errorf("read-back verification failed for %s: BRIS shows %d dated %s, expected %d in %s", item.Label, item.Counter.Previous, item.Counter.Date.Format(time.DateOnly), item.Value, now.Format("2006-01"))
			}
		}
		fmt.Fprintln(out, "BRIS accepted and verified all 4 readings. Gas was not submitted.")
		return nil
	}
	return fmt.Errorf("BRIS submission did not complete")
}

func attr(n *html.Node, name string) string {
	value, _ := attribute(n, name)
	return value
}

func attribute(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

func hasClass(n *html.Node, class string) bool {
	for _, value := range strings.Fields(attr(n, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func find(root *html.Node, predicate func(*html.Node) bool) []*html.Node {
	var result []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && predicate(n) {
			result = append(result, n)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func ancestor(n *html.Node, tag string) *html.Node {
	for n = n.Parent; n != nil; n = n.Parent {
		if n.Type == html.ElementNode && n.Data == tag {
			return n
		}
	}
	return nil
}

func textContent(root *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return strings.Join(strings.Fields(text.String()), " ")
}

func hiddenFields(root *html.Node) url.Values {
	fields := url.Values{}
	for _, input := range find(root, func(n *html.Node) bool { return n.Data == "input" && attr(n, "type") == "hidden" }) {
		if _, disabled := attribute(input, "disabled"); !disabled && attr(input, "name") != "" {
			fields.Add(attr(input, "name"), attr(input, "value"))
		}
	}
	return fields
}

func parseRussianDate(value string, zone *time.Location) (time.Time, error) {
	parts := strings.Fields(value)
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	if len(parts) == 3 {
		for i, month := range months {
			if parts[1] == month {
				date, err := time.ParseInLocation("2-1-2006", fmt.Sprintf("%s-%d-%s", parts[0], i+1, parts[2]), zone)
				if err == nil {
					return date, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid BRIS reading date %q", value)
}
