package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGasHTTPWorkflow(t *testing.T) {
	accountHTML, err := os.ReadFile("testdata/account.html")
	if err != nil {
		t.Fatal(err)
	}
	gasHTML, err := os.ReadFile("testdata/gas.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                                                                           string
		alternate, dry, wrongAccount, wrongMeter, decrease, dropWrite, reject, network bool
		wantPosts                                                                      int
		wantError                                                                      string
	}{
		{name: "primary account", wantPosts: 1},
		{name: "alternate account overrides default account", alternate: true, wantPosts: 1},
		{name: "dry run", dry: true},
		{name: "wrong account", wrongAccount: true, wantError: "different gas account"},
		{name: "wrong meter", wrongMeter: true, wantError: "differs from configured"},
		{name: "lower than transmitted", decrease: true, wantError: "below portal"},
		{name: "success without persistence", dropWrite: true, wantPosts: 1, wantError: "read-back mismatch"},
		{name: "rejection", reject: true, wantPosts: 1, wantError: "did not confirm"},
		{name: "uncertain outcome is not replayed", network: true, wantPosts: 1, wantError: "outcome is unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := gasReading{Routing: gasAccount{"000000000001", "100001"}, Value: 123, Date: time.Date(2000, 10, 18, 0, 0, 0, 0, time.UTC)}
			previous, current, initials := int64(100), int64(100), "ТЕА"
			if test.alternate {
				r.Routing = gasAccount{"000000000002", "100002"}
				r.Value = 234
				previous = 200
				current = 200
				initials = "ТЕБ"
			}
			if test.decrease {
				current = r.Value + 1
			}
			posts, selections, readsAfterPost := 0, 0, 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				session, _ := req.Cookie("session")
				if session == nil || session.Value != "fake-session" {
					http.Error(w, "unauthenticated", http.StatusUnauthorized)
					return
				}
				switch req.URL.Path {
				case "/gas":
					http.Redirect(w, req, "/gas/account-number?globalsid=test-session", http.StatusFound)
				case "/gas/account-number":
					fmt.Fprint(w, string(accountHTML)+`<div hidden><button id="enter_button">Войти</button></div>`)
				case "/gas/account_number":
					selections++
					const prefix = "hcs_gas_input_barcode_model"
					if req.FormValue(prefix+"[account_number]") != r.Routing.Account || req.FormValue(prefix+"[account_choices]") != r.Routing.Account || req.FormValue(prefix+"[fio]") != initials || req.FormValue("__csrf_test") != "test-token" || req.FormValue("globalsid") != "test-token" || req.FormValue(prefix+"[is_default]") != "0" {
						t.Error("account selection, initials, CSRF or session fields differ from portal contract")
						http.Error(w, "bad selection", 400)
						return
					}
					http.Redirect(w, req, "/gas/check-service?globalsid=test-session", http.StatusFound)
				case "/gas/check-service":
					fmt.Fprint(w, `<a href="/gas/meter-readings-data?globalsid=test-session">Подать показания счетчиков</a>`)
				case "/gas/meter-readings-data":
					if posts > 0 {
						readsAfterPost++
					}
					body := strings.NewReplacer("000000000001", r.Routing.Account, "100001", r.Routing.Meter).Replace(string(gasHTML))
					body = strings.Replace(body, `name="hcs_gas_model[counters][0][prev_value]" type="hidden" value="100"`, `name="hcs_gas_model[counters][0][prev_value]" type="hidden" value="`+strconv.FormatInt(previous, 10)+`"`, 1)
					body = strings.Replace(body, `name="hcs_gas_model[counters][0][current_value]" type="hidden" value="123"`, `name="hcs_gas_model[counters][0][current_value]" type="hidden" value="`+strconv.FormatInt(current, 10)+`"`, 1)
					if test.wrongAccount {
						body = strings.ReplaceAll(body, r.Routing.Account, "000000000099")
					}
					if test.wrongMeter {
						body = strings.ReplaceAll(body, r.Routing.Meter, "999999")
					}
					fmt.Fprint(w, body)
				case "/gas/meter_readings_data":
					posts++
					if test.network {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					const prefix = "hcs_gas_model[counters][0]"
					if req.FormValue("__csrf_test") != "test-token" || req.FormValue("globalsid") != "test-token" || req.FormValue(prefix+"[counter_code]") != r.Routing.Meter || req.FormValue(prefix+"[prev_value]") != strconv.FormatInt(previous, 10) || req.FormValue(prefix+"[current_value]") != strconv.FormatInt(current, 10) || req.FormValue(prefix+"[counter_value]") != strconv.FormatInt(r.Value, 10) || req.FormValue("hcs_gas_model[meter_readings_date]") != "18.10.2000" {
						t.Error("gas submission violates hidden-field, meter, value or reading-date contract")
						http.Error(w, "bad submission", 400)
						return
					}
					var digits strings.Builder
					for i := 1; i <= 6; i++ {
						digits.WriteString(req.FormValue(fmt.Sprintf("c-gas-%d[0]", i)))
					}
					if digits.String() != fmt.Sprintf("%06d", r.Value) {
						t.Errorf("wrong six-digit reading %s", &digits)
						http.Error(w, "bad digits", 400)
						return
					}
					if test.reject {
						fmt.Fprint(w, `<div class="error_explanation">Rejected</div>`)
						return
					}
					if !test.dropWrite {
						current, _ = strconv.ParseInt(req.FormValue(prefix+"[counter_value]"), 10, 64)
					}
					http.Redirect(w, req, "/gas/dmr-complete?globalsid=test-session", http.StatusFound)
				case "/gas/dmr-complete":
					fmt.Fprint(w, `<div class="success_explanation">Показания счетчиков успешно переданы</div>`)
				default:
					http.NotFound(w, req)
				}
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(server.URL)
			jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "fake-session", Path: "/"}})
			client, err := newPortalClient(server.URL, &http.Client{Jar: jar, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = client.gas(context.Background(), r, test.dry, &output)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("want %q, got %v", test.wantError, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if posts != test.wantPosts || selections != 1 {
				t.Errorf("gas POSTs/account selections=%d/%d, want %d/1", posts, selections, test.wantPosts)
			}
			if test.wantError == "" && !test.dry && (readsAfterPost == 0 || !strings.Contains(output.String(), "Gas accepted and verified")) {
				t.Fatalf("missing persisted read-back: %s", &output)
			}
		})
	}
}
