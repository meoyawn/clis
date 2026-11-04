package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
)

// InspectHAR reports schemas only. Private body/header/cookie values never leave
// the offline recording, and no recorded request is replayed.
func InspectHAR(path string) (Object, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("HAR file %q exceeds 64 MiB", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var har struct {
		Log struct {
			Entries []struct {
				Request struct {
					Method   string `json:"method"`
					URL      string `json:"url"`
					PostData struct {
						MimeType string                  `json:"mimeType"`
						Params   []struct{ Name string } `json:"params"`
						Text     string                  `json:"text"`
					} `json:"postData"`
				} `json:"request"`
				Response struct {
					Status int `json:"status"`
				} `json:"response"`
			} `json:"entries"`
		} `json:"log"`
	}
	if json.Unmarshal(body, &har) != nil {
		return nil, fmt.Errorf("invalid HAR JSON in %q", path)
	}
	entries := []any{}
	for _, entry := range har.Log.Entries {
		u, err := url.Parse(entry.Request.URL)
		if err != nil || u.Scheme != "https" || u.Host != "career.habr.com" || u.User != nil {
			continue
		}
		keys := []string{}
		for _, param := range entry.Request.PostData.Params {
			keys = append(keys, param.Name)
		}
		var fields Object
		if json.Unmarshal([]byte(entry.Request.PostData.Text), &fields) == nil {
			for key := range fields {
				keys = append(keys, key)
			}
		} else if entry.Request.PostData.MimeType == "application/x-www-form-urlencoded" {
			if values, err := url.ParseQuery(entry.Request.PostData.Text); err == nil {
				for key := range values {
					keys = append(keys, key)
				}
			}
		}
		slices.Sort(keys)
		query := []string{}
		for key := range u.Query() {
			query = append(query, key)
		}
		slices.Sort(query)
		entries = append(entries, Object{"method": entry.Request.Method, "path": u.EscapedPath(), "status": entry.Response.Status, "request_keys": slices.Compact(keys), "query_keys": query})
	}
	return Object{"requests": entries}, nil
}
