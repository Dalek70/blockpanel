package ai

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Web search via DuckDuckGo's HTML endpoint: free, no API key, no account.
// HTML scraping is inherently best-effort; failures return a clear error the
// model can relay.

var searchClient = &http.Client{Timeout: 15 * time.Second}

var (
	resultRe  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRe = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>|<td[^>]+class="result-snippet"[^>]*>(.*?)</td>`)
	tagRe     = regexp.MustCompile(`<[^>]*>`)
)

type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

func WebSearch(ctx context.Context, query string) ([]SearchResult, error) {
	q := url.QueryEscape(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("empty query")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://html.duckduckgo.com/html/?q="+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0")
	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("search returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	page := string(body)

	links := resultRe.FindAllStringSubmatch(page, 8)
	snippets := snippetRe.FindAllStringSubmatch(page, 8)

	var out []SearchResult
	for i, m := range links {
		if len(out) >= 5 {
			break
		}
		r := SearchResult{
			Title: cleanHTML(m[2]),
			URL:   decodeDDGLink(m[1]),
		}
		if i < len(snippets) {
			s := snippets[i][1]
			if s == "" {
				s = snippets[i][2]
			}
			r.Snippet = cleanHTML(s)
		}
		if r.URL != "" && r.Title != "" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no results parsed (DuckDuckGo may be rate-limiting or changed markup)")
	}
	return out, nil
}

// decodeDDGLink unwraps DDG's /l/?uddg=<real-url> redirect links.
func decodeDDGLink(raw string) string {
	raw = html.UnescapeString(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.Contains(u.Host, "duckduckgo.com") {
		if real := u.Query().Get("uddg"); real != "" {
			return real
		}
	}
	return raw
}

func cleanHTML(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// FormatSearchResults renders results as tool output text.
func FormatSearchResults(results []SearchResult) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return b.String()
}
