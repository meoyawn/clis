package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service/transaction"
)

var redesignedEntry = regexp.MustCompile(`https://abs\.twimg\.com/x-web/x-web/entry-client-[A-Za-z0-9_-]+\.js`)
var staticModuleImport = regexp.MustCompile(`from["'](\./(?:assets/)?[A-Za-z0-9_-]+\.js)["']`)
var lazyModuleImport = regexp.MustCompile("import\\(`(\\./[A-Za-z0-9_-]+\\.js)`\\)")
var signerModuleImport = regexp.MustCompile("import\\(`(\\./[A-Za-z0-9_-]+\\.js)`\\)\\.then\\(\\w+=>\\w+\\.default\\(\\)\\)")
var createTweetMetadata = regexp.MustCompile("params:\\{id:`([A-Za-z0-9_-]+)`,metadata:\\{\\},name:`CreateTweet`,operationKind:`mutation`,text:null,providedVariables:\\{([^}]+)\\}")
var relayVariableName = regexp.MustCompile(`(__relay_internal__pv__[A-Za-z0-9_]+):`)
var authorItemsImport = regexp.MustCompile("import\\(`(\\./[A-Za-z0-9_-]+\\.js)`\\),__vite__mapDeps\\(\\[[0-9,]*\\]\\)\\),`AuthorItems`")
var deleteTweetMetadata = regexp.MustCompile("params:\\{id:`([A-Za-z0-9_-]+)`,metadata:\\{\\},name:`deleteTweetMutation`,operationKind:`mutation`,text:null\\}")

func (c *Client) publicModule(ctx context.Context, base, reference string) (string, []byte, error) {
	parent, err := url.Parse(base)
	if err != nil {
		return "", nil, err
	}
	relative, err := url.Parse(reference)
	if err != nil {
		return "", nil, err
	}
	target := parent.ResolveReference(relative)
	if target.Scheme != "https" || target.Host != "abs.twimg.com" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || !strings.HasPrefix(target.Path, "/x-web/x-web/") {
		return "", nil, fmt.Errorf("unsupported X public module %q", target.String())
	}
	if data, ok := c.publicModules[target.String()]; ok {
		return target.String(), data, nil
	}
	data, err := c.request(ctx, http.MethodGet, target.String(), "", nil)
	if err != nil {
		return "", nil, err
	}
	if c.publicModules == nil {
		c.publicModules = map[string][]byte{}
	}
	c.publicModules[target.String()] = data
	if c.bearer == "" {
		if token := publicBearer.FindString(string(data)); token != "" {
			c.bearer, err = url.QueryUnescape(token)
			if err != nil {
				return "", nil, fmt.Errorf("invalid X public client authorization encoding")
			}
		}
	}
	return target.String(), data, nil
}

func (c *Client) prepareRedesignedWrites(ctx context.Context) error {
	entry := redesignedEntry.FindString(c.bootstrapDocument)
	if entry == "" {
		return fmt.Errorf("the redesigned X bootstrap has no public entry module")
	}
	entryURL, source, err := c.publicModule(ctx, entry, "")
	if err != nil {
		return err
	}
	imports := staticModuleImport.FindAllSubmatch(source, -1)
	if len(imports) == 0 || len(imports) > 20 {
		return fmt.Errorf("the redesigned X entry has an unsupported module graph")
	}
	var routesURL string
	var routes []byte
	for _, dependency := range imports {
		moduleURL, module, err := c.publicModule(ctx, entryURL, string(dependency[1]))
		if err != nil {
			return err
		}
		if bytes.Contains(module, []byte("path:`/compose/post`")) {
			routesURL, routes = moduleURL, module
		}
	}
	if routesURL == "" || c.bearer == "" {
		return fmt.Errorf("the redesigned X modules do not expose the composer and public client authorization")
	}
	composerStart := bytes.Index(routes, []byte("path:`/compose/post`"))
	composerImport := lazyModuleImport.FindSubmatch(routes[composerStart:])
	signerImport := signerModuleImport.FindSubmatch(routes)
	if composerImport == nil || signerImport == nil {
		return fmt.Errorf("the redesigned X modules do not expose the composer and signing imports")
	}
	composerURL, composer, err := c.publicModule(ctx, routesURL, string(composerImport[1]))
	if err != nil {
		return err
	}
	metadata := createTweetMetadata.FindSubmatch(composer)
	for _, dependency := range staticModuleImport.FindAllSubmatch(composer, -1) {
		if metadata != nil {
			break
		}
		_, module, err := c.publicModule(ctx, composerURL, string(dependency[1]))
		if err != nil {
			return err
		}
		metadata = createTweetMetadata.FindSubmatch(module)
	}
	if metadata == nil {
		return fmt.Errorf("the redesigned X composer has no CreateTweet mutation metadata")
	}
	_, signer, err := c.publicModule(ctx, routesURL, string(signerImport[1]))
	if err != nil {
		return err
	}
	generator, err := transaction.New(c.bootstrapDocument, string(signer))
	if err != nil {
		return fmt.Errorf("prepare redesigned X per-request signing metadata: %w", err)
	}
	c.operations = map[string]string{domain.CreateOperation().String(): string(metadata[1])}
	c.relayVariables = map[string]bool{}
	// Optional response fragments stay disabled; publication is verified with
	// a separate read of the exact post, including its text, image, and parent.
	for _, variable := range relayVariableName.FindAllSubmatch(metadata[2], -1) {
		name := string(variable[1])
		c.relayVariables[name] = strings.Contains(name, "appviewerisloggedin")
	}
	c.transaction = generator
	return nil
}

func (c *Client) prepareDeletion(ctx context.Context) error {
	if !c.redesigned {
		return fmt.Errorf("post deletion requires the inspected x-web client")
	}
	if err := c.PrepareWriteMetadata(ctx); err != nil {
		return err
	}
	if c.operations[domain.DeleteOperation().String()] != "" {
		return nil
	}
	for base, source := range c.publicModules {
		dependency := authorItemsImport.FindSubmatch(source)
		if dependency == nil {
			continue
		}
		_, module, err := c.publicModule(ctx, base, string(dependency[1]))
		if err != nil {
			return err
		}
		metadata := deleteTweetMetadata.FindSubmatch(module)
		if metadata == nil {
			return fmt.Errorf("the X author menu has no deleteTweetMutation metadata")
		}
		c.operations[domain.DeleteOperation().String()] = string(metadata[1])
		return nil
	}
	return fmt.Errorf("the X public modules do not expose the author menu import")
}
