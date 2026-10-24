package service

import (
	"net/url"
	"testing"
)

func TestBrandPredictionPreservesExplicitConstraints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want, got string
		allowed         bool
	}{
		{"prediction", "text=test", "text=test&brand=100&brand_was_predicted=true", true},
		{"canonical prediction flag", "text=test", "text=test&brand=100&brand_was_predicted=t", true},
		{"preserve explicit brand", "text=test&brand=100", "text=test&brand=100&brand_was_predicted=true", true},
		{"unmarked brand", "text=test", "text=test&brand=100", false},
		{"disabled prediction", "text=test", "text=test&brand=100&brand_was_predicted=false", false},
		{"invalid brand", "text=test", "text=test&brand=unknown&brand_was_predicted=true", false},
		{"replace explicit brand", "text=test&brand=100", "text=test&brand=200&brand_was_predicted=true", false},
		{"replace adopted brand", "text=test&brand=100&brand_was_predicted=true", "text=test&brand=200&brand_was_predicted=true", false},
		{"drop brand", "text=test&brand=100", "text=test&brand_was_predicted=true", false},
		{"drop explicit filter", "text=test&delivery=4", "text=test&brand=100&brand_was_predicted=true", false},
		{"change sorting", "text=test&sorting=price", "text=test&sorting=score&brand=100&brand_was_predicted=true", false},
		{"change query", "text=test", "text=other&brand=100&brand_was_predicted=true", false},
		{"add other filter", "text=test", "text=test&delivery=4&brand=100&brand_was_predicted=true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want, err := url.ParseQuery(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := url.ParseQuery(tc.got)
			if err != nil {
				t.Fatal(err)
			}
			before := want.Encode()
			if err := sameSearchRedirectConstraints(want, got); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
			if want.Encode() != before {
				t.Fatal("redirect validation mutated the caller's filters")
			}
		})
	}
}
