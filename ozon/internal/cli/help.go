package cli

import (
	"bytes"
	"flag"
	"fmt"
)

const usage = `Usage: go run . <command>

Commands:
  auth     Import or check authentication
  search   Search products
  filters  List search filters and sorting options
  show     Show product details and seller offers
  reviews  Read buyer reviews
  cart     Manage the shopping cart

All prices are in RUB.
When Ozon rejects the saved anti-bot cookie, any command loads ozon.ru in a
hidden system web view, which runs Ozon's current check, saves the renewed
cookie and resends the rejected request once. Your browser is not used.
`

const authUsage = `Usage: go run . auth <command>

Commands:
  import  Import a browser session
  status  Check the saved session
`

const cartUsage = `Usage: go run . cart <command>

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
If filters are missing, use go run . filters with the same query and filters.
Result rating covers all variants of a product; for this SKU's review count use go run . show.

Continue the same query with --cursor using next_cursor from the previous response.
Do not combine --cursor with --page, --filter or --sort. --limit trims this page.

Examples:
  go run . search 'running shoes' --include-filters --limit 10
  go run . search 'running shoes' --filter 'currency_price=0;5000' --sort price
  go run . search 'running shoes' --cursor '<next_cursor>'`
	case "filters":
		syntax = "filters <query> [options]"
		details = `Print available search filters and sorting options as JSON.
Use filter keys and option values with --filter, and sorting values with --sort.
Repeat --filter for different keys; quote semicolon-separated values and ranges.

Examples:
  go run . filters 'running shoes'
  go run . filters 'running shoes' --filter 'currency_price=0;5000'`
	case "show":
		syntax = "show <sku-or-url> [options]"
		details = `Print product prices, availability, selected variants and the current seller as JSON.
Use --full for additional description, characteristics and cheaper/faster seller offers.
Check the selected variant and both Ozon-card and regular payment prices.
sku_review_count counts only this SKU's reviews (one extra review-page request);
rating is Ozon's rating across all variants, which has no per-SKU equivalent.
Shared product URLs keep their sh parameter for the current offer and delivery estimate.

Examples:
  go run . show 1613489220
  go run . show https://www.ozon.ru/product/1613489220/ --full`
	case "reviews":
		syntax = "reviews <sku-or-url> [options]"
		details = `Print one page of buyer reviews as JSON.
By default, reviews and total cover this SKU; --scope all includes other variants.
rating is Ozon's rating across all variants in both scopes.

Continue the same product with --cursor using next_cursor from the previous response.
Do not combine --cursor with --page, --scope or --sort. --limit trims this page.

Examples:
  go run . reviews 1613489220 --sort score_asc --limit 10
  go run . reviews 1613489220 --scope all
  go run . reviews 1613489220 --cursor '<next_cursor>'`
	case "auth import":
		syntax = "auth import --browser BROWSER [options]"
		details = `Copy Ozon cookies from a signed-in browser to the CLI's private config directory.
Sign in to Ozon in the browser first. Import makes no HTTP requests.

Examples:
  go run . auth import --browser chrome
  go run . auth import --browser chrome --browser-profile 'Profile 1'`
	case "auth status":
		syntax = "auth status [options]"
		details = `Check the saved Ozon session without updating it or reading browser cookies.
Use --json for structured authentication details.

Example:
  go run . auth status --json`
	case "cart add":
		syntax = "cart add <sku-or-url> [options]"
		details = `Add one unit of the specified SKU to the saved session's cart and print JSON.
If the request fails, check go run . cart list before repeating it.

Example:
  go run . cart add 1613489220`
	case "cart remove":
		syntax = "cart remove <sku-or-url> [options]"
		details = `Remove all units of the specified SKU from the saved session's cart and print JSON.
If the request fails, check go run . cart list before repeating it.

Example:
  go run . cart remove 1613489220`
	case "cart list":
		syntax = "cart list [options]"
		details = `Print cart items, quantities and payment price tiers as JSON.
Recommendation widgets are excluded.

Example:
  go run . cart list`
	default:
		panic(fmt.Sprintf("missing help for command %q", flags.Name()))
	}

	var help bytes.Buffer
	fmt.Fprintf(&help, "Usage: go run . %s\n\n%s\n\nOptions:\n", syntax, details)
	output := flags.Output()
	flags.SetOutput(&help)
	flags.PrintDefaults()
	flags.SetOutput(output)
	fmt.Fprintln(&help, "\nFlags may appear before or after arguments; -- ends option parsing.")
	_, err := output.Write(help.Bytes())
	return err
}
