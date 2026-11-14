package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOrganicKeywordReportPreservesMetricsAndRestrictedAccess(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "report", true: "unverified domain"}[restricted], func(t *testing.T) {
			httpClient := &http.Client{}
			requests := 0
			routeTo(t, httpClient, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/v4/seGetOrganicKeywords" {
					t.Errorf("unexpected keyword request: %s %s", r.Method, r.URL.Path)
				}
				var input struct {
					Args struct {
						URL     string `json:"url"`
						Country string `json:"country"`
					} `json:"args"`
					Params struct {
						Size   int `json:"size"`
						Offset int `json:"offset"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(r.URL.Query().Get("input")), &input); err != nil {
					t.Error(err)
				}
				if input.Args.URL != "example.test/" || input.Args.Country != "ca" || input.Params.Size != 1 || input.Params.Offset != 2 {
					t.Errorf("wrong report scope: %+v", input)
				}
				w.Header().Set("Content-Type", "application/json")
				if restricted {
					_, _ = io.WriteString(w, `["Error","SeDomainIsNotVerified"]`)
					return
				}
				_, _ = io.WriteString(w, `["Ok",{"count":4,"rows":[["example query","CA","0","10","0",null,"0","1","https://example.test/","ai_overview","2026-10-21T12:21:45Z"]]}]`)
			})
			result, err := NewClient(httpClient).OrganicKeywords(t.Context(), KeywordOptions{Domain: "example.test", Country: "ca", Limit: 1, Offset: 2})
			if restricted {
				if err == nil || !strings.Contains(err.Error(), "domain is not verified") || result != nil || requests != 1 {
					t.Fatalf("restricted report = %+v, %v; requests = %d", result, err, requests)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Count != 4 || result.NextOffset == nil || *result.NextOffset != 3 || len(result.Rows) != 1 || requests != 1 {
				t.Fatalf("report = %+v; requests = %d", result, requests)
			}
			row := result.Rows[0]
			if string(row["volume"]) != `"0"` || string(row["difficulty"]) != `"0"` || string(row["cpc"]) != "null" || string(row["best_position_kind"]) != `"ai_overview"` {
				t.Fatalf("metric semantics changed: %+v", row)
			}
		})
	}
}
