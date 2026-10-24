package cli

import (
	"strings"
	"testing"
)

func TestProductReferences(t *testing.T) {
	for _, s := range []string{"2467", testProductID, "https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/", "https://invitro.ru/analizes/profi/912/69795/", "https://www.invitro.ru/analizes/checkups/kazan/programmy-zdorovya/" + testProductID + "/"} {
		if _, err := parseReference(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "-1", "0", "199USK", "../1", "https://www.invitro.ru.evil.test/analizes/for-doctors/629/2467/", "http://www.invitro.ru/analizes/for-doctors/629/2467/", "https://user@www.invitro.ru/analizes/for-doctors/629/2467/", "https://www.invitro.ru:444/analizes/for-doctors/629/2467/", "https://www.invitro.ru/analizes/for-doctors/kazan/629/", "https://www.invitro.ru/analizes/for-doctors/kazan/629/%32%34%36%37/"} {
		if _, err := parseReference(s); err == nil {
			t.Errorf("accepted invalid reference %q", s)
		}
	}
}

func TestMarkupPreservesBoundaries(t *testing.T) {
	got := plainText(`<p>First &amp; second<br>Next</p><ul><li>One</li><li>Two</li></ul><table><tr><th>Analyte</th><th>Value</th></tr><tr><td>TSH</td><td>1</td></tr></table><script>do not include</script>`)
	for _, want := range []string{"First & second\nNext", "One\nTwo", "Analyte | Value", "TSH | 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "do not include") {
		t.Error("script included")
	}
}

func TestMissingPricesAreUnknown(t *testing.T) {
	p, err := normalizeProduct(apiProduct{ID: testProductID, BitrixID: 2467, CategoryID: 629, Title: "Test"}, "moscow", Analyses())
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceFrom != nil || p.AtHome != nil || p.Deadline != nil {
		t.Error("missing facts replaced with zero")
	}
	if p.URL != "https://www.invitro.ru/analizes/for-doctors/629/2467/" {
		t.Error("incorrect Moscow URL")
	}
}
