// Derived from github.com/ichioda/x-client-transaction-id (MIT).
// Copyright (c) 2026 George Santiago; see LICENSE.
package transaction

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Generator struct {
	key       []byte
	animation string
}

func New(document, chunk string) (*Generator, error) {
	key, err := parseKey(document)
	if err != nil {
		return nil, err
	}
	frames, err := parseFrames(document)
	if err != nil {
		return nil, err
	}
	first, rest, err := parseIndices(chunk)
	if err != nil {
		return nil, err
	}
	animation, err := deriveAnimationKey(key, frames, first, rest)
	if err != nil {
		return nil, err
	}
	return &Generator{key: key, animation: animation}, nil
}

func (g *Generator) Header(method, path string, now time.Time) (string, error) {
	if method == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return "", fmt.Errorf("transaction header requires method and a pathname without query")
	}
	seconds := (now.UnixMilli() - 1682924400000) / 1000
	if seconds < 0 || seconds > 1<<32-1 {
		return "", fmt.Errorf("transaction time is outside the supported epoch")
	}
	timeBytes := []byte{byte(seconds), byte(seconds >> 8), byte(seconds >> 16), byte(seconds >> 24)}
	sum := sha256.Sum256([]byte(method + "!" + path + "!" + strconv.FormatInt(seconds, 10) + "obfiowerehiring" + g.animation))
	var mask [1]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return "", err
	}
	data := append([]byte{}, g.key...)
	data = append(data, timeBytes...)
	data = append(data, sum[:16]...)
	data = append(data, 3)
	result := []byte{mask[0]}
	for _, b := range data {
		result = append(result, b^mask[0])
	}
	return base64.RawStdEncoding.EncodeToString(result), nil
}
