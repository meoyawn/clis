package service

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

type RequestOptions struct {
	UserAgent string
}

func (c *Client) Search(ctx context.Context, options SearchOptions, auth RequestOptions) (*SearchResult, error) {
	target, err := SearchURL(options)
	if err != nil {
		return nil, err
	}
	request, err := c.Prepare(ctx, target, auth, false)
	if err != nil {
		return nil, err
	}
	response, err := c.Do(request)
	if err != nil {
		return nil, err
	}
	result, err := ParseSearch(response.Body, options)
	if err != nil {
		return nil, err
	}
	stamp(&result.Metadata, request, response)
	if result.IsAuthenticated != nil && !*result.IsAuthenticated {
		result.Warnings = append(result.Warnings, "Avito returned a logged-out response despite saved cookies; refresh them with avito auth import.")
	}
	return result, nil
}

func (c *Client) Show(ctx context.Context, item ItemReference, auth RequestOptions) (*ShowResult, error) {
	if item.URL == "" {
		return nil, fmt.Errorf("listing %q needs a canonical Avito URL", item.ID)
	}
	canonical, err := canonicalListingURL(item.URL, item.ID)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(canonical)
	if err != nil {
		return nil, fmt.Errorf("parse canonical listing URL: %w", err)
	}
	request, err := c.Prepare(ctx, target, auth, true)
	if err != nil {
		return nil, err
	}
	response, err := c.Do(request)
	if err != nil {
		return nil, err
	}
	result, err := ParseShow(response.Body, item.ID)
	if err != nil {
		return nil, err
	}
	stamp(&result.Metadata, request, response)
	if result.IsAuthenticated != nil && !*result.IsAuthenticated {
		result.Warnings = append(result.Warnings, "Avito returned a logged-out response despite saved cookies; refresh them with avito auth import.")
	}
	return result, nil
}

func stamp(meta *Metadata, request *PreparedRequest, response *Exchange) {
	meta.Source = "web"
	meta.SourceURL = request.sourceURL()
	meta.FetchedAt = time.Now().UTC()
	meta.HTTPRequests = 1
	meta.RateHeaders = rateHeaders(response.Headers)
}
