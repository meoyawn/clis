package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type portalClient struct {
	base *url.URL
	http *http.Client
}
type page struct {
	url *url.URL
	doc *html.Node
}
type form struct {
	action *url.URL
	fields url.Values
	node   *html.Node
}
type gasForm struct {
	form              form
	previous, current int64
}

func newPortalClient(origin string, client *http.Client) (*portalClient, error) {
	base, err := url.Parse(origin)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid portal origin; expected HTTP(S) without credentials")
	}
	if client == nil || client.Jar == nil {
		return nil, fmt.Errorf("portal HTTP client must have a session cookie jar")
	}
	c := &portalClient{base, client}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("portal exceeded 10 redirects")
		}
		return c.checkOrigin(req.URL)
	}
	return c, nil
}

func (c *portalClient) checkOrigin(target *url.URL) error {
	if target.Scheme != c.base.Scheme || !strings.EqualFold(target.Host, c.base.Host) || target.User != nil {
		return fmt.Errorf("portal left its configured origin; refresh authentication with auth import")
	}
	return nil
}

func (c *portalClient) request(ctx context.Context, target *url.URL, fields url.Values) (page, error) {
	if err := c.checkOrigin(target); err != nil {
		return page{}, err
	}
	method := http.MethodGet
	var body io.Reader
	if fields != nil {
		method = http.MethodPost
		body = strings.NewReader(fields.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return page{}, fmt.Errorf("create portal request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 uslugi-tatar-cli/1.0")
	req.Header.Set("Accept", "text/html")
	if fields != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", c.base.String())
		req.Header.Set("Referer", target.String())
	}
	response, err := c.http.Do(req)
	if err != nil {
		// url.Error includes query parameters, which can contain session tokens.
		if urlErr, ok := err.(*url.Error); ok {
			err = urlErr.Err
		}
		return page{}, fmt.Errorf("%s %s: %w", method, target.Path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return page{}, fmt.Errorf("%s %s returned HTTP %d", method, target.Path, response.StatusCode)
	}
	const maxBody = 4 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return page{}, fmt.Errorf("read portal response: %w", err)
	}
	if len(data) > maxBody {
		return page{}, fmt.Errorf("portal response exceeds %d bytes", maxBody)
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return page{}, fmt.Errorf("parse portal HTML: %w", err)
	}
	if strings.Contains(response.Request.URL.Path, "/auth/") {
		return page{}, errNotAuthenticated
	}
	return page{response.Request.URL, doc}, nil
}

func (c *portalClient) readForm(p page, inputName, expectedPath string) (form, error) {
	inputs := find(p.doc, func(n *html.Node) bool { return n.Data == "input" || n.Data == "select" })
	var matches []*html.Node
	for _, input := range inputs {
		if attr(input, "name") == inputName {
			matches = append(matches, input)
		}
	}
	if len(matches) != 1 {
		return form{}, fmt.Errorf("portal form field %s: expected one, found %d; run auth status to check the session", inputName, len(matches))
	}
	var node *html.Node
	for parent := matches[0].Parent; parent != nil; parent = parent.Parent {
		if parent.Data == "form" {
			node = parent
			break
		}
	}
	if node == nil || !strings.EqualFold(attr(node, "method"), "post") {
		return form{}, fmt.Errorf("portal POST form for %s is missing", inputName)
	}
	action, err := p.url.Parse(attr(node, "action"))
	if err != nil {
		return form{}, fmt.Errorf("invalid portal form action")
	}
	if err := c.checkOrigin(action); err != nil {
		return form{}, err
	}
	if action.Path != expectedPath {
		return form{}, fmt.Errorf("portal form action %q changed; expected %s", action.Path, expectedPath)
	}
	fields := url.Values{}
	for _, input := range find(node, func(n *html.Node) bool { return n.Data == "input" && attr(n, "type") == "hidden" }) {
		if _, disabled := attribute(input, "disabled"); !disabled && attr(input, "name") != "" {
			fields.Add(attr(input, "name"), attr(input, "value"))
		}
	}
	return form{action, fields, node}, nil
}

func (c *portalClient) gasPage(ctx context.Context, routing gasAccount) (page, error) {
	start, err := c.request(ctx, c.base.ResolveReference(&url.URL{Path: "/gas"}), nil)
	if err != nil {
		return page{}, err
	}
	const prefix = "hcs_gas_input_barcode_model"
	accountForm, err := c.readForm(start, prefix+"[account_choices]", "/gas/account_number")
	if err != nil {
		return page{}, err
	}
	accounts, err := savedGasAccounts(start)
	if err != nil {
		return page{}, err
	}
	var account savedGasAccount
	for _, candidate := range accounts {
		if candidate.Account == routing.Account {
			account = candidate
		}
	}
	if account.Account == "" || account.FIO == "" || account.Mode == "old" {
		return page{}, fmt.Errorf("gas account %s is unavailable in this session's saved accounts", routing.Account)
	}
	accountForm.fields.Set(prefix+"[account_choices]", routing.Account)
	accountForm.fields.Set(prefix+"[account_number]", routing.Account)
	accountForm.fields.Set(prefix+"[fio]", account.FIO)
	accountForm.fields.Set(prefix+"[find_type]", "account_number")
	accountForm.fields.Set(prefix+"[is_default]", "0")
	services, err := c.request(ctx, accountForm.action, accountForm.fields)
	if err != nil {
		return page{}, fmt.Errorf("select gas account: %w", err)
	}
	if services.url.Path != "/gas/check-service" {
		return page{}, fmt.Errorf("portal did not open services for gas account %s", routing.Account)
	}
	link, err := c.link(services, "/gas/meter-readings-data")
	if err != nil {
		return page{}, err
	}
	return c.request(ctx, link, nil)
}

func (c *portalClient) link(p page, path string) (*url.URL, error) {
	var links []*url.URL
	for _, a := range find(p.doc, func(n *html.Node) bool { return n.Data == "a" }) {
		link, err := p.url.Parse(attr(a, "href"))
		if err == nil && link.Path == path {
			if err := c.checkOrigin(link); err != nil {
				return nil, err
			}
			links = append(links, link)
		}
	}
	if len(links) != 1 {
		return nil, fmt.Errorf("portal link %s: expected one, found %d", path, len(links))
	}
	return links[0], nil
}

func (c *portalClient) parseGas(p page, routing gasAccount) (gasForm, error) {
	const prefix = "hcs_gas_model[counters][0]"
	f, err := c.readForm(p, prefix+"[counter_code]", "/gas/meter_readings_data")
	if err != nil {
		return gasForm{}, err
	}
	info := find(f.node, func(n *html.Node) bool { return hasClass(n, "gas-billing-information") })
	accountPattern := regexp.MustCompile(`Лицевой счет:\s*([0-9]{12})\b`)
	if len(info) != 1 {
		return gasForm{}, fmt.Errorf("gas account confirmation is missing")
	}
	account := accountPattern.FindStringSubmatch(textContent(info[0]))
	if len(account) != 2 || account[1] != routing.Account {
		return gasForm{}, fmt.Errorf("portal opened a different gas account; expected %s", routing.Account)
	}
	codes := find(f.node, func(n *html.Node) bool {
		return n.Data == "input" && strings.HasSuffix(attr(n, "name"), "[counter_code]")
	})
	if len(codes) != 1 || f.fields.Get(prefix+"[counter_code]") != routing.Meter {
		return gasForm{}, fmt.Errorf("portal gas meter differs from configured serial %s", routing.Meter)
	}
	previous, err := strconv.ParseInt(f.fields.Get(prefix+"[prev_value]"), 10, 64)
	if err != nil || previous < 0 {
		return gasForm{}, fmt.Errorf("portal gas meter %s has invalid previous value", routing.Meter)
	}
	current, err := strconv.ParseInt(f.fields.Get(prefix+"[current_value]"), 10, 64)
	if err != nil || current < 0 {
		return gasForm{}, fmt.Errorf("portal gas meter %s has invalid current value", routing.Meter)
	}
	for digit := 1; digit <= 6; digit++ {
		name := fmt.Sprintf("c-gas-%d[0]", digit)
		if len(find(f.node, func(n *html.Node) bool { return n.Data == "input" && attr(n, "name") == name })) != 1 {
			return gasForm{}, fmt.Errorf("portal gas digit field %s is missing", name)
		}
	}
	return gasForm{f, previous, current}, nil
}

func (c *portalClient) gas(ctx context.Context, r gasReading, dryRun bool, out io.Writer) error {
	p, err := c.gasPage(ctx, r.Routing)
	if err != nil {
		return err
	}
	gas, err := c.parseGas(p, r.Routing)
	if err != nil {
		return err
	}
	if r.Value < gas.previous || r.Value < gas.current {
		return fmt.Errorf("gas reading %d is below portal accounted/transmitted values %d/%d", r.Value, gas.previous, gas.current)
	}
	fmt.Fprintf(out, "Gas account %s; meter %s; reading date %s\nAccounted %d -> %d m³ (usage %d m³); latest transmitted %d\n", r.Routing.Account, r.Routing.Meter, r.Date.Format(timeDateOnly), gas.previous, r.Value, r.Value-gas.previous, gas.current)
	printGasHistory(out, r)
	if dryRun {
		fmt.Fprintln(out, "Dry run: no gas readings submitted.")
		return nil
	}
	const prefix = "hcs_gas_model[counters][0]"
	gas.form.fields.Set(prefix+"[counter_value]", strconv.FormatInt(r.Value, 10))
	digits := fmt.Sprintf("%06d", r.Value)
	for digit := 1; digit <= 6; digit++ {
		gas.form.fields.Set(fmt.Sprintf("c-gas-%d[0]", digit), digits[digit-1:digit])
	}
	gas.form.fields.Set("hcs_gas_model[meter_readings_date]", r.Date.Format("02.01.2006"))
	result, err := c.request(ctx, gas.form.action, gas.form.fields)
	if err != nil {
		return fmt.Errorf("gas submission outcome is unknown; not retrying automatically: %w; inspect with --dry-run", err)
	}
	accepted := find(result.doc, func(n *html.Node) bool {
		return hasClass(n, "success_explanation") && textContent(n) == "Показания счетчиков успешно переданы"
	})
	if result.url.Path != "/gas/dmr-complete" || len(accepted) == 0 {
		return fmt.Errorf("portal did not confirm gas submission; inspect with --dry-run before retrying")
	}
	saved, err := c.request(ctx, p.url, nil)
	if err != nil {
		return fmt.Errorf("gas accepted, but read-back failed: %w", err)
	}
	verified, err := c.parseGas(saved, r.Routing)
	if err != nil {
		return fmt.Errorf("gas read-back: %w", err)
	}
	if verified.current != r.Value {
		return fmt.Errorf("gas read-back mismatch: portal transmitted %d, expected %d", verified.current, r.Value)
	}
	fmt.Fprintf(out, "Gas accepted and verified: account %s, meter %s, value %d m³.\n", r.Routing.Account, r.Routing.Meter, r.Value)
	return nil
}

const timeDateOnly = "2006-01-02"

func attr(n *html.Node, name string) string { value, _ := attribute(n, name); return value }
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
