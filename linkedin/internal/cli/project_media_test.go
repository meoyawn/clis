package cli

import "testing"

func TestProjectThumbnailBelongsToItsRowWhenItPrecedesTheTitle(t *testing.T) {
	const source = `<main><section>
	<div><a href="https://first.example/article"><div aria-label="Thumbnail for First article • 1 of 1"></div></a>
	<a href="/in/example/details/projects/edit/forms/41/">First project</a></div>
	<div><a href="https://second.example/article"><div aria-label="Thumbnail for Second article • 1 of 1"></div></a>
	<a href="/in/example/details/projects/edit/forms/42/">Second project</a></div></section></main>`
	got, err := projectMediaLinks(source, "https://www.linkedin.com/in/example/", "42")
	if err != nil || len(got) != 1 || got[0] != (profileLink{Text: "Second article", URL: "https://second.example/article"}) {
		t.Fatalf("project cards: %+v, %v", got, err)
	}
}

func TestPositionMediaListPairsCaptionWithExpandedExternalLink(t *testing.T) {
	data := readFlightData(`0:{"direction":"horizontal","children":["$Lb",["$","div",null,{"children":[["$","p",null,{"children":["Example CLI"]}],["$","p",null,{"children":["Source code"]}]]}]]}
b:{"urlValue":{"url":"https://www.linkedin.com/safety/go/?url=https%3A%2F%2Fexample.org%2Fcli"}}
`)
	got, err := parsePositionMediaList(data, 1)
	if err != nil || len(got) != 1 || got[0] != (profileLink{Text: "Example CLI", URL: "https://example.org/cli"}) {
		t.Fatalf("expanded cards: %+v, %v", got, err)
	}
	if _, err := parsePositionMediaList(data, 2); err == nil {
		t.Fatal("accepted an incomplete media list")
	}
}
