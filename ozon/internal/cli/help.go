package cli

import (
	"bytes"
	"flag"
	"fmt"
)

const usage = `Usage: ozon <command>

Commands:
  auth     Import or check authentication
  search   Search products
  filters  List search filters and sorting options
  show     Show product details and seller offers
  reviews  Read buyer reviews
  cart     Manage the shopping cart

All prices are in RUB.
`

const authUsage = `Usage: ozon auth <command>

Commands:
  import  Import a browser session
  status  Check the saved session
`

const cartUsage = `Usage: ozon cart <command>

Commands:
  add     Add one unit of a product
  remove  Remove all units of a product
  list    List cart items
`

func printCommandUsage(flags *flag.FlagSet) error {
	var syntax, details string
	switch flags.Name() {
	case "search":
		syntax = "search <query> [options]"
		details = `Search products and print one page as JSON.
Use --include-filters to discover filter keys, option values and sorting values.
Pass those values with --filter and --sort; query words do not enforce filters.
Repeat --filter for different keys; quote semicolon-separated values and ranges.
If filters are missing, use ozon filters with the same query and filters.

Continue the same query with --cursor using next_cursor from the previous response.
Do not combine --cursor with --page, --filter or --sort. --limit trims this page.

Examples:
  ozon search 'running shoes' --include-filters --limit 10
  ozon search 'running shoes' --filter 'currency_price=0;5000' --sort price
  ozon search 'running shoes' --cursor '<next_cursor>'`
	case "filters":
		syntax = "filters <query> [options]"
		details = `Print available search filters and sorting options as JSON.
Use filter keys and option values with --filter, and sorting values with --sort.
Repeat --filter for different keys; quote semicolon-separated values and ranges.

Examples:
  ozon filters 'running shoes'
  ozon filters 'running shoes' --filter 'currency_price=0;5000'`
	case "show":
		syntax = "show <sku-or-url> [options]"
		details = `Print product prices, availability, selected variants and the current seller as JSON.
Use --full for additional description, characteristics and cheaper/faster seller offers.
Check the selected variant and both Ozon-card and regular payment prices.

Examples:
  ozon show 1613489220
  ozon show https://www.ozon.ru/product/1613489220/ --full`
	case "reviews":
		syntax = "reviews <sku-or-url> [options]"
		details = `Print one page of buyer reviews as JSON.
By default, reviews cover this SKU; --scope all includes other variants.

Continue the same product with --cursor using next_cursor from the previous response.
Do not combine --cursor with --page, --scope or --sort. --limit trims this page.

Examples:
  ozon reviews 1613489220 --sort score_asc --limit 10
  ozon reviews 1613489220 --scope all
  ozon reviews 1613489220 --cursor '<next_cursor>'`
	case "auth import":
		syntax = "auth import --browser BROWSER [options]"
		details = `Copy Ozon cookies from a signed-in browser to the CLI's private config directory.
Sign in to Ozon in the browser first. Import makes no HTTP requests.

Examples:
  ozon auth import --browser chrome
  ozon auth import --browser chrome --browser-profile 'Profile 1'`
	case "auth status":
		syntax = "auth status [options]"
		details = `Check the saved Ozon session without updating it or reading browser cookies.
Use --json for structured authentication details.

Example:
  ozon auth status --json`
	case "cart add":
		syntax = "cart add <sku-or-url> [options]"
		details = `Add one unit of the specified SKU to the saved session's cart and print JSON.
If the request fails, check ozon cart list before repeating it.

Example:
  ozon cart add 1613489220`
	case "cart remove":
		syntax = "cart remove <sku-or-url> [options]"
		details = `Remove all units of the specified SKU from the saved session's cart and print JSON.
If the request fails, check ozon cart list before repeating it.

Example:
  ozon cart remove 1613489220`
	case "cart list":
		syntax = "cart list [options]"
		details = `Print cart items, quantities and payment price tiers as JSON.
Recommendation widgets are excluded.

Example:
  ozon cart list`
	default:
		panic(fmt.Sprintf("missing help for command %q", flags.Name()))
	}

	var help bytes.Buffer
	fmt.Fprintf(&help, "Usage: ozon %s\n\n%s\n\nOptions:\n", syntax, details)
	output := flags.Output()
	flags.SetOutput(&help)
	flags.PrintDefaults()
	flags.SetOutput(output)
	fmt.Fprintln(&help, "\nFlags may appear before or after arguments; -- ends option parsing.")
	_, err := output.Write(help.Bytes())
	return err
}
