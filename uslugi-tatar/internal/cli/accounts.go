package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/html"
)

type savedGasAccount struct {
	Account, Label string
	FIO            string `json:"fio"`
	Mode           string `json:"mode"`
}

type discoveredAccount struct {
	Label, Account, Note string
	Main                 *mainAccount
	Gas                  *gasAccount
	Informer             string
}

func savedGasAccounts(p page) ([]savedGasAccount, error) {
	const prefix = "hcs_gas_input_barcode_model"
	data := find(p.doc, func(n *html.Node) bool { return attr(n, "id") == prefix+"_accounts" })
	selects := find(p.doc, func(n *html.Node) bool { return n.Data == "select" && attr(n, "name") == prefix+"[account_choices]" })
	if len(data) != 1 || len(selects) != 1 {
		return nil, fmt.Errorf("portal saved gas accounts are missing or ambiguous")
	}
	var metadata map[string]savedGasAccount
	if err := json.Unmarshal([]byte(attr(data[0], "value")), &metadata); err != nil {
		return nil, fmt.Errorf("portal saved gas account metadata is invalid")
	}
	var accounts []savedGasAccount
	seen := map[string]bool{}
	for _, option := range find(selects[0], func(n *html.Node) bool { return n.Data == "option" }) {
		number := attr(option, "value")
		if number == "" || number == "0" {
			continue
		}
		account, ok := metadata[number]
		if !ok || seen[number] || !regexp.MustCompile(`^[0-9]{12}$`).MatchString(number) {
			return nil, fmt.Errorf("saved gas account selection and metadata disagree")
		}
		account.Account, account.Label = number, textContent(option)
		accounts = append(accounts, account)
		seen[number] = true
	}
	return accounts, nil
}

func discoveredMain(p page, informer string) (mainAccount, error) {
	infos := find(p.doc, func(n *html.Node) bool { return hasClass(n, "account_info") })
	if len(infos) != 1 {
		return mainAccount{}, fmt.Errorf("no confirmed ЖКХ account details; open this card in the browser to finish setup")
	}
	text := textContent(infos[0])
	account := regexp.MustCompile(`Лицевой счет:\s*([0-9]{10})\b`).FindStringSubmatch(text)
	flat := regexp.MustCompile(`квар\.\s*([0-9]+)\b`).FindStringSubmatch(text)
	address := regexp.MustCompile(`Адрес:\s*(.+?),\s*квар\.`).FindStringSubmatch(text)
	if len(account) != 2 || len(flat) != 2 || len(address) != 2 {
		return mainAccount{}, fmt.Errorf("account, address or apartment could not be confirmed; no YAML recommendation")
	}
	return mainAccount{Account: account[1], Flat: flat[1], Address: strings.TrimSpace(address[1]), Informer: informer}, nil
}

func (c *portalClient) accounts(ctx context.Context) ([]discoveredAccount, error) {
	if err := c.checkAuth(ctx); err != nil {
		return nil, err
	}
	cards, err := c.request(ctx, c.base.ResolveReference(&url.URL{Path: "/ajax/user/get-informers"}), nil)
	if err != nil {
		return nil, fmt.Errorf("load saved ЖКХ cards: %w", err)
	}
	var result []discoveredAccount
	seen := map[string]bool{}
	for _, a := range find(cards.doc, func(n *html.Node) bool { return n.Data == "a" }) {
		link, err := cards.url.Parse(attr(a, "href"))
		if err != nil || link.Path != "/personal-account/init-by-user-tsj" || link.Query().Get("informer_type") != "jkh_payment" {
			continue
		}
		if err := c.checkOrigin(link); err != nil {
			return nil, err
		}
		id := link.Query().Get("informer_id")
		if !regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`).MatchString(id) {
			return nil, fmt.Errorf("saved ЖКХ card has an invalid informer UUID")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		candidate := discoveredAccount{Informer: id, Label: "ЖКХ"}
		labels := find(a, func(n *html.Node) bool { return hasClass(n, "informer-name") })
		if len(labels) == 1 {
			candidate.Label = textContent(labels[0])
		}
		// Build the request from the verified ID, without copying arbitrary card queries.
		init := c.base.ResolveReference(&url.URL{Path: link.Path, RawQuery: url.Values{"informer_id": {id}, "informer_type": {"jkh_payment"}}.Encode()})
		p, err := c.request(ctx, init, nil)
		if err != nil {
			candidate.Note = err.Error()
		} else if main, err := discoveredMain(p, id); err != nil {
			candidate.Note = err.Error()
		} else {
			candidate.Main, candidate.Account = &main, main.Account
		}
		result = append(result, candidate)
	}
	start, err := c.request(ctx, c.base.ResolveReference(&url.URL{Path: "/gas"}), nil)
	if err != nil {
		return nil, fmt.Errorf("load saved gas accounts: %w", err)
	}
	gasAccounts, err := savedGasAccounts(start)
	if err != nil {
		return nil, err
	}
	for _, account := range gasAccounts {
		candidate := discoveredAccount{Label: account.Label, Account: account.Account}
		if account.Mode == "old" || account.FIO == "" {
			candidate.Note = "saved gas account needs updating in the browser"
			result = append(result, candidate)
			continue
		}
		p, err := c.gasPage(ctx, gasAccount{Account: account.Account})
		if err != nil {
			candidate.Note = err.Error()
		} else {
			codes := find(p.doc, func(n *html.Node) bool {
				return n.Data == "input" && strings.HasSuffix(attr(n, "name"), "[counter_code]")
			})
			if len(codes) != 1 || !regexp.MustCompile(`^[0-9]+$`).MatchString(attr(codes[0], "value")) {
				candidate.Note = fmt.Sprintf("expected one gas meter serial, found %d; no YAML recommendation", len(codes))
			} else {
				routing := gasAccount{Account: account.Account, Meter: attr(codes[0], "value")}
				if _, err := c.parseGas(p, routing); err != nil {
					candidate.Note = err.Error()
				} else {
					candidate.Gas = &routing
				}
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

func printAccounts(out io.Writer, accounts []discoveredAccount, asYAML bool, number string) error {
	var selected []discoveredAccount
	for _, account := range accounts {
		if number == "" || account.Account == number {
			selected = append(selected, account)
		}
	}
	if number != "" && len(selected) == 0 {
		return fmt.Errorf("account %q was not found; run accounts to list saved accounts", number)
	}
	if asYAML {
		var documents []string
		for _, account := range selected {
			if account.Main == nil && account.Gas == nil {
				continue
			}
			document := struct {
				Uslugi struct {
					Main *mainAccount `yaml:"main,omitempty"`
					Gas  *gasAccount  `yaml:"gas,omitempty"`
				} `yaml:"uslugi"`
			}{}
			document.Uslugi.Main, document.Uslugi.Gas = account.Main, account.Gas
			data, err := yaml.Marshal(document)
			if err != nil {
				return fmt.Errorf("encode account recommendation: %w", err)
			}
			documents = append(documents, "# "+terminalText(account.Label)+"\n"+string(data))
		}
		if len(documents) == 0 {
			return fmt.Errorf("no usable routing found; run accounts for account details")
		}
		if len(documents) > 1 {
			fmt.Fprintln(out, "# Separate candidates: choose ONE document with --account <number>.")
		}
		_, err := io.WriteString(out, strings.Join(documents, "---\n"))
		return err
	}
	if len(selected) == 0 {
		fmt.Fprintln(out, "No saved ЖКХ or gas accounts found. Add them on the portal first.")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tNAME\tACCOUNT\tDETAILS")
	for _, account := range selected {
		service, details := "gas", account.Note
		if account.Informer != "" {
			service = "main (ЖКХ)"
			details = "informer " + account.Informer + "; " + account.Note
		}
		if account.Main != nil {
			details = fmt.Sprintf("%s; apartment %s; informer %s", account.Main.Address, account.Main.Flat, account.Main.Informer)
		} else if account.Gas != nil {
			details = "meter " + account.Gas.Meter
		}
		// Normalize remote labels before writing a terminal table.
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", service, terminalText(account.Label), account.Account, terminalText(details))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nCopy suggested routing with: accounts --account <number> --yaml")
	fmt.Fprintln(out, "Merge the chosen main/gas blocks under one uslugi key in your apartment's meters.yaml. Add your measured readings separately.")
	return nil
}

func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

func runAccounts(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("accounts", flag.ContinueOnError)
	flags.SetOutput(errOut)
	asYAML := flags.Bool("yaml", false, "print suggested uslugi routing blocks for meters.yaml (separate YAML documents)")
	number := flags.String("account", "", "show only this saved account number")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("accounts does not accept positional arguments; use --account <number>")
	}
	client, err := authenticatedClient()
	if err != nil {
		return err
	}
	accounts, err := client.accounts(ctx)
	if err != nil {
		return err
	}
	return printAccounts(out, accounts, *asYAML, *number)
}
