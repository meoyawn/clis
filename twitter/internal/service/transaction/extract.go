// Derived from github.com/ichioda/x-client-transaction-id (MIT).
// Copyright (c) 2026 George Santiago; see LICENSE.
package transaction

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const onDemandURLTemplate = "https://abs.twimg.com/responsive-web/client-web/ondemand.s.%sa.js"

var (
	reKey      = regexp.MustCompile(`<meta\s+name="twitter-site-verification"\s+content="([^"]+)"`)
	reChunkID  = regexp.MustCompile(`,(\d+):"ondemand\.s"`)
	reFrameSVG = regexp.MustCompile(`<svg[^>]*id="loading-x-anim-(\d)"[^>]*>(.*?)</svg>`)
	rePathD    = regexp.MustCompile(`<path\s+d="([^"]*)"`)
	reDigits   = regexp.MustCompile(`[0-9]+`)
	reIndices  = regexp.MustCompile(`\(\w\[(\d{1,2})\],\s*16\)`)
)

func parseKey(doc string) ([]byte, error) {
	m := reKey.FindStringSubmatch(doc)
	if m == nil {
		return nil, fmt.Errorf("twitter-site-verification meta tag not found")
	}
	key, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != 48 {
		return nil, fmt.Errorf("verification key must contain 48 bytes; got %d", len(key))
	}
	return key, nil
}

func ChunkURL(doc string) (string, string, error) {
	m := reChunkID.FindStringSubmatch(doc)
	if m == nil {
		return "", "", fmt.Errorf("ondemand.s chunk id not found in document")
	}
	reHash := regexp.MustCompile(`,` + m[1] + `:"([0-9a-f]+)"`)
	h := reHash.FindStringSubmatch(doc)
	if h == nil {
		return "", "", fmt.Errorf("ondemand.s hash not found for chunk %s", m[1])
	}
	return fmt.Sprintf(onDemandURLTemplate, h[1]), h[1], nil
}

func parseIndices(chunk string) (int, []int, error) {
	matches := reIndices.FindAllStringSubmatch(chunk, -1)
	if len(matches) < 2 {
		return 0, nil, fmt.Errorf("key byte indices not found in ondemand.s")
	}
	first, err := strconv.Atoi(matches[0][1])
	if err != nil {
		return 0, nil, err
	}
	rest := make([]int, 0, len(matches)-1)
	for _, m := range matches[1:] {
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, nil, err
		}
		rest = append(rest, v)
	}
	return first, rest, nil
}

func parseFrames(doc string) ([][][]int, error) {
	matches := reFrameSVG.FindAllStringSubmatch(doc, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("loading-x-anim frames not found")
	}
	byIndex := map[int]string{}
	for _, m := range matches {
		idx, _ := strconv.Atoi(m[1])
		byIndex[idx] = m[2]
	}
	frames := make([][][]int, 0, len(byIndex))
	for i := 0; i < len(byIndex); i++ {
		inner, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("frame loading-x-anim-%d missing", i)
		}
		paths := rePathD.FindAllStringSubmatch(inner, -1)
		if len(paths) < 2 {
			return nil, fmt.Errorf("frame %d: animation path missing", i)
		}
		frames = append(frames, parsePathD(paths[1][1]))
	}
	return frames, nil
}

func parsePathD(d string) [][]int {
	if len(d) > 9 {
		d = d[9:]
	}
	var rows [][]int
	for _, seg := range strings.Split(d, "C") {
		nums := reDigits.FindAllString(seg, -1)
		if len(nums) == 0 {
			continue
		}
		row := make([]int, len(nums))
		for i, n := range nums {
			row[i], _ = strconv.Atoi(n)
		}
		rows = append(rows, row)
	}
	return rows
}
