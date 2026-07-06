// Package anttool exposes ant's URI front door as tomo tools. ant mounts every
// tamnd/*-cli domain behind one resource-URI space, so a single get(uri) reaches
// the whole fleet: reddit://user/foo, goodreads://book/123, x://status/456. The
// caller builds a kit.Host from whichever domain drivers it compiled in (each
// registers itself from init, like a database/sql driver) and hands it here.
package anttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamnd/any-cli/kit"

	"github.com/tamnd/tomo/pkg/tool"
)

// Tools returns the ant front-door tools over host: get to dereference a URI,
// ls to list a collection, and search to find records by text within a domain.
// limit caps how many records ls and search return, 0 for the domain's own
// default. With no domains mounted the tools still register but report that
// nothing is reachable, which is a clearer failure than a missing tool.
func Tools(host *kit.Host, limit int) []tool.Tool {
	return []tool.Tool{
		getTool(host),
		lsTool(host, limit),
		searchTool(host, limit),
	}
}

func getTool(host *kit.Host) tool.Tool {
	return tool.Tool{
		Name: "ant_get",
		Description: "Fetch one record by its resource URI from any mounted site, such as " +
			"reddit://user/spez or goodreads://book/3735293. Also accepts a pasted https URL " +
			"from a site ant knows. Returns the record as JSON.",
		Class: tool.ClassNet,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"uri": {"type": "string", "description": "a resource URI (scheme://authority/id) or a known site URL"}
			},
			"required": ["uri"]
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			u, err := host.Resolve(strings.TrimSpace(v.URI))
			if err != nil {
				return "", err
			}
			rec, err := host.Get(ctx, u)
			if err != nil {
				return "", err
			}
			return marshal(rec)
		},
	}
}

func lsTool(host *kit.Host, limit int) tool.Tool {
	return tool.Tool{
		Name: "ant_ls",
		Description: "List the members of a collection URI from any mounted site, such as " +
			"reddit://subreddit/golang or a series that lists its books. Returns the records as JSON.",
		Class: tool.ClassNet,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"uri": {"type": "string", "description": "a collection resource URI"},
				"limit": {"type": "integer", "description": "cap on how many members to return"}
			},
			"required": ["uri"]
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v struct {
				URI   string `json:"uri"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			u, err := host.Resolve(strings.TrimSpace(v.URI))
			if err != nil {
				return "", err
			}
			recs, err := host.List(ctx, u, pick(v.Limit, limit))
			if err != nil {
				return "", err
			}
			return marshal(recs)
		},
	}
}

func searchTool(host *kit.Host, limit int) tool.Tool {
	return tool.Tool{
		Name: "ant_search",
		Description: "Search a mounted site by free text and return the matching records as JSON. " +
			"Name the site by its scheme, such as reddit or goodreads.",
		Class: tool.ClassNet,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scheme": {"type": "string", "description": "the site's URI scheme, such as reddit"},
				"query": {"type": "string", "description": "the free-text query"},
				"limit": {"type": "integer", "description": "cap on how many results to return"}
			},
			"required": ["scheme", "query"]
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Scheme string `json:"scheme"`
				Query  string `json:"query"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			v.Scheme = strings.TrimSpace(v.Scheme)
			v.Query = strings.TrimSpace(v.Query)
			if v.Scheme == "" || v.Query == "" {
				return "", fmt.Errorf("scheme and query are both required")
			}
			if !host.Searchable(v.Scheme) {
				return "", fmt.Errorf("site %q has no search", v.Scheme)
			}
			recs, err := host.Search(ctx, v.Scheme, v.Query, pick(v.Limit, limit))
			if err != nil {
				return "", err
			}
			return marshal(recs)
		},
	}
}

// pick prefers a per-call limit over the tool's default; either 0 means no cap.
func pick(call, dflt int) int {
	if call > 0 {
		return call
	}
	return dflt
}

func marshal(v any) (string, error) {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(buf), nil
}
