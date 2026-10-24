package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestAccountDiscoveryAndRecommendations(t *testing.T) {
	accountHTML, err := os.ReadFile("testdata/account.html")
	if err != nil {
		t.Fatal(err)
	}
	gasHTML, err := os.ReadFile("testdata/gas.html")
	if err != nil {
		t.Fatal(err)
	}
	mainHTML, err := os.ReadFile("testdata/main_services.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, wrongGas := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong gas identity=%v", wrongGas), func(t *testing.T) {
			var selections atomic.Int32
			selected := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				switch req.URL.Path {
				case "/":
					fmt.Fprint(w, `<a href="/user/logout">Logout</a>`)
				case "/ajax/user/get-informers":
					fmt.Fprint(w, `<li><a href="/personal-account/init-by-user-tsj?informer_id=00000000-0000-4000-8000-000000000001&amp;informer_type=jkh_payment"><div class="informer-name">Apartment A</div></a></li>
<li><a href="/personal-account/init-by-user-tsj?informer_id=00000000-0000-4000-8000-000000000002&amp;informer_type=jkh_payment"><div class="informer-name">Unconfigured card</div></a></li>`)
				case "/personal-account/init-by-user-tsj":
					if req.URL.Query().Get("informer_type") != "jkh_payment" {
						t.Error("wrong informer type")
					}
					if req.URL.Query().Get("informer_id") == "00000000-0000-4000-8000-000000000001" {
						fmt.Fprint(w, string(mainHTML))
					} else {
						fmt.Fprint(w, `<p>Please finish setting up this account</p>`)
					}
				case "/gas":
					fmt.Fprint(w, string(accountHTML))
				case "/gas/account_number":
					selections.Add(1)
					selected = req.FormValue("hcs_gas_input_barcode_model[account_choices]")
					initials := "ТЕА"
					if selected == "000000000002" {
						initials = "ТЕБ"
					}
					if req.Method != http.MethodPost || req.FormValue("__csrf_test") != "test-token" || req.FormValue("hcs_gas_input_barcode_model[is_default]") != "0" || req.FormValue("hcs_gas_input_barcode_model[fio]") != initials {
						t.Error("discovery must preserve CSRF, account initials and the default account")
					}
					http.Redirect(w, req, "/gas/check-service", http.StatusFound)
				case "/gas/check-service":
					fmt.Fprint(w, `<a href="/gas/meter-readings-data">Meters</a>`)
				case "/gas/meter-readings-data":
					serial := "100001"
					if selected == "000000000002" {
						serial = "100002"
					}
					body := strings.NewReplacer("000000000001", selected, "100001", serial).Replace(string(gasHTML))
					if wrongGas {
						body = strings.ReplaceAll(body, selected, "000000000099")
					}
					fmt.Fprint(w, body)
				default:
					t.Errorf("discovery attempted an unexpected request: %s %s", req.Method, req.URL.Path)
					http.NotFound(w, req)
				}
				if req.Method != http.MethodGet && req.URL.Path != "/gas/account_number" {
					t.Error("discovery must never post meter readings or payments")
				}
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			client, err := newPortalClient(server.URL, &http.Client{Jar: jar, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			accounts, err := client.accounts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(accounts) != 4 || selections.Load() != 2 {
				t.Fatalf("discovery missed cards/accounts: %d results, %d gas selections", len(accounts), selections.Load())
			}
			var table bytes.Buffer
			if err := printAccounts(&table, accounts, false, ""); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(table.String(), "Apartment A") || !strings.Contains(table.String(), "Unconfigured card") || !strings.Contains(table.String(), "finish setup") {
				t.Fatalf("list must retain unconfigured cards with an explanation: %s", &table)
			}
			var output bytes.Buffer
			if err := printAccounts(&output, accounts, true, "0000000001"); err != nil {
				t.Fatal(err)
			}
			var document meterDocument
			if err := yaml.Unmarshal(output.Bytes(), &document); err != nil {
				t.Fatalf("recommendation is not valid meter YAML: %v", err)
			}
			main := document.Uslugi.Main
			if main.Account != "0000000001" || main.Flat != "1" || main.Address != "Город, ул ПРИМЕРНАЯ, дом 1" || main.Informer != "00000000-0000-4000-8000-000000000001" || document.Uslugi.Gas.Account != "" {
				t.Fatalf("incorrect main routing: %+v", document.Uslugi)
			}
			for i, number := range []string{"000000000001", "000000000002"} {
				output.Reset()
				err := printAccounts(&output, accounts, true, number)
				if wrongGas {
					if err == nil || output.Len() != 0 {
						t.Fatal("unconfirmed gas account must never generate routing")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				document = meterDocument{}
				if err := yaml.Unmarshal(output.Bytes(), &document); err != nil {
					t.Fatal(err)
				}
				if document.Uslugi.Gas.Account != number || document.Uslugi.Gas.Meter != fmt.Sprintf("10000%d", i+1) || document.Uslugi.Main.Account != "" {
					t.Fatalf("incorrect gas routing: %+v", document.Uslugi)
				}
			}
			output.Reset()
			if err := printAccounts(&output, accounts, true, "0000000099"); err == nil || output.Len() != 0 {
				t.Fatal("unknown account filter must fail without generating YAML")
			}
		})
	}
}
