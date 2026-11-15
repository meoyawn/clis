# Tasty Coffee

```sh
go run ./tastycoffee --help
go run ./tastycoffee auth import --browser chrome --browser-profile Default
go run ./tastycoffee auth status --check
go run ./tastycoffee account
go run ./tastycoffee orders --current
go run ./tastycoffee latest
```

Sign in to <https://shop.tastycoffee.ru/> in Chrome first. Import uses
[sweetcookie](https://github.com/steipete/sweetcookie) to read only the
`auth.token` cookie from the selected Chrome profile. The session is saved
outside the repository in the OS user config directory, under
`tastycoffee/session.json`, with owner-only file permissions. Cookie values
are never printed. Only `auth import` accesses Chrome or the OS keychain.

`auth status` checks the local file. `--check` also verifies it against the
account API. Other commands read the saved session without refreshing it.

Prices are rubles. Order products retain their historical price and selected
options; `prices` includes the actual discount and delivery lines. Nested
current catalog prices, images, SEO text, and buyer contact data are excluded.
`orders` lists history; `--current` selects active orders. The site serves
these as separate lists, so `latest` checks both. History includes cancelled
orders. Pagination is server-side.

This CLI reads account/order data. Ordering remains in the browser. No command
creates or changes orders, subscriptions, saved payment methods, or addresses.

## Observed API

The browser flow was inspected on 2026-11-15. Authenticated calls use the
`auth.token` cookie as an `Authorization: Bearer` token. The CLI sends it only
to the fixed HTTPS origin and refuses redirects.

| Operation | Endpoint |
| --- | --- |
| Account and personal discount | `GET /api/v2/me?with_legal=true` |
| Historical orders | `GET /api/v2/account/orders?limit=N&page=N` |
| Active orders | Same endpoint with `current=true` |
| Repeat into browser cart | `POST /api/v2/cart/sync` |
| Price calculation | `POST /api/v2/cart` |
| Apply a promo code | `POST /api/v2/cart/coupon` |
| Available payment methods | `POST /api/v2/cart/payment-methods` |
| Available delivery methods | `POST /api/v2/cart/deliveries` |
| Place an order | `POST /api/v2/checkout` |
| Checkout result | `GET /api/v2/checkout/{request_id}/status` |
| Cancel an order | `POST /api/v2/account/orders/{order_id}/cancel` |

Observed cart requests wrap their payload in `cart`. Item strings have the
form `product_id::option_value_ids::quantity::0`, with option IDs separated
by commas. Pickup delivery uses `delivery_type: "pvz"`, a shipping code,
an account address ID, and a separate `pvz_id`; the account address is not the
pickup address. `payment_code: "cod"` means payment on delivery/pickup.
Checkout returns a request identifier that the browser polls for its result.
Never retry checkout blindly: a lost response can still mean an order exists.

Discount eligibility is dynamic. The site's [discount policy](https://shop.tastycoffee.ru/discount-system)
caps the loyalty discount at 15%; loyalty and coupon discounts do not stack.
Pack pricing can reduce the amount before the order-level discount. Compare
the final payable amount including delivery; do not add advertised discount
percentages. Historical order data is not a quote for a future purchase.

The observed [17% Привет! offer](https://vamprivet.ru/promo/produkty-pitaniya/kofe-svezhey-obzharki-3/)
requires a unique, single-use promo code claimed from a signed-in Привет!
account. Saving a Мир card at Tasty Coffee does not claim or apply that code.
Check the offer's current terms and the recalculated basket before payment.
The price calculation response reports `coupon_percent`, `coupon`,
`personal_discount`, and `shipping` separately. A successfully applied coupon
does not establish that checkout completed; verify the created order's price
lines and paid status. A saved card is referenced by its payment provider's
saved-card code, not by card numbers.

Private network captures and imported sessions are kept outside this module.
All test data is synthetic.
