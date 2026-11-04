package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/service"
)

func TestHARInspectionEmitsContractsNotCredentialOrBodyValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.har")
	input := `{"log":{"entries":[
		{"request":{"method":"PATCH","url":"https://career.habr.com/api/frontend/vacancies/1000000000/responses/123?token=PRIVATE_QUERY","headers":[{"name":"Cookie","value":"PRIVATE_COOKIE"}],"postData":{"mimeType":"multipart/form-data","params":[{"name":"body","value":"PRIVATE_LETTER"},{"name":"authenticity_token","value":"PRIVATE_TOKEN"}]}},"response":{"status":200,"content":{"text":"PRIVATE_RESPONSE"}}},
		{"request":{"method":"GET","url":"https://foreign.test/PRIVATE_FOREIGN"},"response":{"status":200}}
	]}}`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.InspectHAR(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "PRIVATE_") || !strings.Contains(string(b), `"request_keys":["authenticity_token","body"]`) || !strings.Contains(string(b), `"status":200`) || !strings.Contains(string(b), `"query_keys":["token"]`) {
		t.Fatalf("HAR contract/privacy violation: %s", b)
	}
}
