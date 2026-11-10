// Package openlibrary searches Open Library (https://openlibrary.org) to
// auto-fill book details. No API key is needed.
package openlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Result struct {
	Key          string `json:"key"` // e.g. /works/OL45804W
	Title        string `json:"title"`
	Authors      string `json:"authors"`
	Year         int    `json:"year,omitempty"`
	CoverURL     string `json:"cover_url,omitempty"`
	GoodreadsURL string `json:"goodreads_url,omitempty"`
}

type Client struct {
	HTTP *http.Client
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 8 * time.Second}}
}

type searchResponse struct {
	Docs []struct {
		Key              string   `json:"key"`
		Title            string   `json:"title"`
		AuthorName       []string `json:"author_name"`
		FirstPublishYear int      `json:"first_publish_year"`
		CoverI           int      `json:"cover_i"`
		IDGoodreads      []string `json:"id_goodreads"`
	} `json:"docs"`
}

func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	u := "https://openlibrary.org/search.json?" + url.Values{
		"q":      {query},
		"limit":  {"10"},
		"fields": {"key,title,author_name,first_publish_year,cover_i,id_goodreads"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Open Library asks clients to identify themselves.
	req.Header.Set("User-Agent", "SelfHostedBookClub/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open library returned %s", resp.Status)
	}
	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(sr.Docs))
	for _, d := range sr.Docs {
		r := Result{
			Key:     d.Key,
			Title:   d.Title,
			Authors: strings.Join(d.AuthorName, ", "),
			Year:    d.FirstPublishYear,
		}
		if d.CoverI > 0 {
			r.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", d.CoverI)
		}
		if len(d.IDGoodreads) > 0 && d.IDGoodreads[0] != "" {
			r.GoodreadsURL = "https://www.goodreads.com/book/show/" + url.PathEscape(d.IDGoodreads[0])
		}
		out = append(out, r)
	}
	return out, nil
}

// GoodreadsSearchURL links to a Goodreads search for books without a known ID.
func GoodreadsSearchURL(title, authors string) string {
	q := strings.TrimSpace(title + " " + authors)
	return "https://www.goodreads.com/search?q=" + url.QueryEscape(q)
}
