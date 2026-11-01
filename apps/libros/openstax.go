package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenStax: libros de texto universitarios con licencia Creative Commons, en
// PDF (unos 130 en inglés y una decena en español: cálculo, física, química,
// estadística…). La API devuelve el catálogo entero de un idioma, que es
// pequeño: se guarda un día y se busca en él.

type openstaxBook struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	State       string `json:"book_state"`
	PDF         string `json:"high_resolution_pdf_url"`
	License     string `json:"license_name"`
	Published   string `json:"publish_date"`
	Description string `json:"description"`
	Authors     []struct {
		Value struct {
			Name string `json:"name"`
		} `json:"value"`
	} `json:"authors"`
	Meta struct {
		Slug   string `json:"slug"`
		Locale string `json:"locale"`
	} `json:"meta"`
}

// openstaxLocales son los idiomas con libros en OpenStax que entiende la app.
var openstaxLocales = []string{"es", "en"}

func (c *catalog) openstaxList(ctx context.Context, locale string) ([]openstaxBook, error) {
	key := "openstax|" + locale
	c.mu.Lock()
	if e, ok := c.lists[key]; ok && time.Since(e.at) < 24*time.Hour {
		c.mu.Unlock()
		return e.books, nil
	}
	c.mu.Unlock()
	var out struct {
		Items []openstaxBook `json:"items"`
	}
	q := url.Values{"type": {"books.Book"}, "locale": {locale}, "limit": {"250"},
		"fields": {"title,book_state,high_resolution_pdf_url,license_name,publish_date,description,authors"}}
	if err := c.getJSON(ctx, c.urls.OpenStax+"/apps/cms/api/v2/pages/?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	var books []openstaxBook
	for _, b := range out.Items {
		// Solo los vigentes, con PDF y licencia libre.
		if b.State == "live" && b.PDF != "" && strings.Contains(b.License, "Creative Commons") {
			books = append(books, b)
		}
	}
	sort.Slice(books, func(i, j int) bool { return books[i].Title < books[j].Title })
	c.mu.Lock()
	if c.lists == nil {
		c.lists = map[string]listEntry{}
	}
	c.lists[key] = listEntry{at: time.Now(), books: books}
	c.mu.Unlock()
	return books, nil
}

type listEntry struct {
	at    time.Time
	books []openstaxBook
}

func (b openstaxBook) item() catalogItem {
	it := catalogItem{Source: "openstax", ID: strconv.Itoa(b.ID), Title: b.Title, Authors: []string{}, Language: b.Meta.Locale,
		Year: yearOf(b.Published), Description: clean(stripTags(b.Description), 600)}
	for _, a := range b.Authors {
		if a.Value.Name != "" {
			it.Authors = append(it.Authors, a.Value.Name)
		}
	}
	return it
}

func (c *catalog) searchOpenStax(ctx context.Context, q, lang string, page int) (catalogPage, error) {
	locales := openstaxLocales
	if lang != "" {
		locales = nil
		for _, l := range openstaxLocales {
			if l == lang {
				locales = []string{l}
			}
		}
	}
	words := searchWords(q)
	var items []catalogItem
	for _, l := range locales {
		books, err := c.openstaxList(ctx, l)
		if err != nil {
			return catalogPage{}, err
		}
		for _, b := range books {
			it := b.item()
			have := map[string]bool{}
			for _, w := range searchWords(it.Title + " " + strings.Join(it.Authors, " ")) {
				have[w] = true
			}
			match := true
			for _, w := range words {
				if !have[w] {
					match = false
					break
				}
			}
			if match {
				items = append(items, it)
			}
		}
	}
	const per = 20
	start := (page - 1) * per
	if start >= len(items) {
		return catalogPage{Items: []catalogItem{}}, nil
	}
	end := min(start+per, len(items))
	return catalogPage{Items: items[start:end], More: end < len(items)}, nil
}

func (c *catalog) resolveOpenStax(ctx context.Context, id string) (download, error) {
	for _, l := range openstaxLocales {
		books, err := c.openstaxList(ctx, l)
		if err != nil {
			return download{}, err
		}
		for _, b := range books {
			if strconv.Itoa(b.ID) != id {
				continue
			}
			u, err := url.Parse(b.PDF)
			if err != nil || !c.allowed(u) { // https y en un dominio de la lista
				return download{}, bad("la descarga no está en un dominio permitido")
			}
			name := b.Meta.Slug
			if name == "" {
				name = fmt.Sprintf("openstax-%d", b.ID)
			}
			return download{url: b.PDF, fileName: name + ".pdf", meta: b.item()}, nil
		}
	}
	return download{}, bad("ese libro no está en OpenStax (o ya no tiene licencia libre)")
}
