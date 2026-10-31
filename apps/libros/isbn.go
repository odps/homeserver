package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"homeserver/platform/app"
)

// normalizeISBN acepta un ISBN-10 o ISBN-13 con guiones, espacios o el
// prefijo urn:isbn:, comprueba el dígito de control y lo devuelve como ISBN-13.
func normalizeISBN(s string) (string, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "urn:isbn:")
	s = strings.TrimPrefix(s, "isbn")
	var d []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			d = append(d, c)
		case c == 'x' && len(d) == 9:
			d = append(d, 'X')
		case c == '-' || c == ' ' || c == ':':
		default:
			return "", false
		}
	}
	switch len(d) {
	case 10:
		sum := 0
		for i, c := range d {
			v := int(c - '0')
			if c == 'X' {
				v = 10
			}
			sum += v * (10 - i)
		}
		if sum%11 != 0 {
			return "", false
		}
		d13 := append([]byte("978"), d[:9]...)
		return string(append(d13, isbn13Check(d13))), true
	case 13:
		if (string(d[:3]) != "978" && string(d[:3]) != "979") || isbn13Check(d) != d[12] {
			return "", false
		}
		return string(d), true
	}
	return "", false
}

// isbn13Check calcula el dígito de control de los 12 primeros dígitos.
func isbn13Check(d []byte) byte {
	sum := 0
	for i, c := range d[:12] {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(c-'0') * w
	}
	return byte('0' + (10-sum%10)%10)
}

// isbnInfo son los datos que se encuentran de un ISBN en Open Library y, para
// lo que falte, en Google Books.
type isbnInfo struct {
	ISBN        string   `json:"isbn"`
	Title       string   `json:"title,omitempty"`
	Authors     []string `json:"authors"`
	Publisher   string   `json:"publisher,omitempty"`
	Published   string   `json:"published,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	Language    string   `json:"language,omitempty"`
	Description string   `json:"description,omitempty"`
	HasCover    bool     `json:"has_cover"`
	coverURL    string
}

// lookupISBN consulta las dos fuentes a la vez y guarda el resultado 10
// minutos (al guardar la portada se vuelve a pedir).
func (c *catalog) lookupISBN(ctx context.Context, isbn string) (isbnInfo, error) {
	c.mu.Lock()
	if e, ok := c.isbns[isbn]; ok && time.Since(e.at) < 10*time.Minute {
		c.mu.Unlock()
		return e.info, nil
	}
	c.mu.Unlock()
	info, err := c.fetchISBN(ctx, isbn)
	if err == nil {
		c.mu.Lock()
		if len(c.isbns) > 200 {
			c.isbns = map[string]isbnEntry{}
		}
		c.isbns[isbn] = isbnEntry{at: time.Now(), info: info}
		c.mu.Unlock()
	}
	return info, err
}

type isbnEntry struct {
	at   time.Time
	info isbnInfo
}

func (c *catalog) fetchISBN(ctx context.Context, isbn string) (isbnInfo, error) {
	info := isbnInfo{ISBN: isbn, Authors: []string{}}
	var ol map[string]struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Authors  []struct {
			Name string `json:"name"`
		} `json:"authors"`
		Publishers []struct {
			Name string `json:"name"`
		} `json:"publishers"`
		PublishDate string `json:"publish_date"`
		Subjects    []struct {
			Name string `json:"name"`
		} `json:"subjects"`
		Cover struct {
			Large string `json:"large"`
		} `json:"cover"`
	}
	var gb googleVolumes
	var olErr, gbErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		olErr = c.getJSON(ctx, c.urls.OpenLibrary+"/api/books?"+url.Values{"bibkeys": {"ISBN:" + isbn}, "format": {"json"}, "jscmd": {"data"}}.Encode(), &ol)
	}()
	go func() {
		defer wg.Done()
		gbErr = c.getJSON(ctx, c.urls.GoogleBooks+"/books/v1/volumes?"+url.Values{"q": {"isbn:" + isbn}}.Encode(), &gb)
	}()
	wg.Wait()
	if b, ok := ol["ISBN:"+isbn]; ok {
		info.Title = b.Title
		if b.Subtitle != "" {
			info.Title += ": " + b.Subtitle
		}
		for _, a := range b.Authors {
			info.Authors = append(info.Authors, a.Name)
		}
		if len(b.Publishers) > 0 {
			info.Publisher = b.Publishers[0].Name
		}
		info.Published = yearOf(b.PublishDate)
		for _, s := range b.Subjects {
			if len(info.Genres) < 5 {
				info.Genres = append(info.Genres, s.Name)
			}
		}
		info.coverURL = b.Cover.Large
	}
	if len(gb.Items) > 0 {
		v := gb.Items[0].VolumeInfo
		if info.Title == "" {
			info.Title = v.Title
			if v.Subtitle != "" {
				info.Title += ": " + v.Subtitle
			}
		}
		if len(info.Authors) == 0 {
			info.Authors = append(info.Authors, v.Authors...)
		}
		if info.Publisher == "" {
			info.Publisher = v.Publisher
		}
		if info.Published == "" {
			info.Published = yearOf(v.PublishedDate)
		}
		if len(info.Genres) == 0 {
			info.Genres = v.Categories
		}
		info.Language = v.Language
		info.Description = stripTags(v.Description)
		if info.coverURL == "" && v.ImageLinks.Thumbnail != "" {
			info.coverURL = strings.Replace(v.ImageLinks.Thumbnail, "http://", "https://", 1)
		}
	}
	if info.Title == "" {
		if olErr != nil && gbErr != nil {
			return info, olErr
		}
		return info, errNotFound
	}
	info.HasCover = info.coverURL != ""
	return info, nil
}

type googleVolumes struct {
	Items []struct {
		VolumeInfo struct {
			Title         string   `json:"title"`
			Subtitle      string   `json:"subtitle"`
			Authors       []string `json:"authors"`
			Publisher     string   `json:"publisher"`
			PublishedDate string   `json:"publishedDate"`
			Description   string   `json:"description"`
			Categories    []string `json:"categories"`
			Language      string   `json:"language"`
			ImageLinks    struct {
				Thumbnail string `json:"thumbnail"`
			} `json:"imageLinks"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

// yearOf saca el año de fechas como «1999», «March 1999» o «1999-03-01».
func yearOf(s string) string {
	isDigit := func(i int) bool { return i >= 0 && i < len(s) && s[i] >= '0' && s[i] <= '9' }
	for i := 0; i+4 <= len(s); i++ {
		if isDigit(i) && isDigit(i+1) && isDigit(i+2) && isDigit(i+3) && !isDigit(i-1) && !isDigit(i+4) {
			return s[i : i+4]
		}
	}
	return ""
}

func (a *api) isbnLookup(w http.ResponseWriter, r *http.Request, who app.Identity) {
	isbn, ok := normalizeISBN(r.PathValue("isbn"))
	if !ok {
		a.fail(w, bad("el ISBN no es válido (revisa los dígitos)"))
		return
	}
	info, err := a.catalog.lookupISBN(r.Context(), isbn)
	if errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no se han encontrado datos de ese ISBN"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// putCover cambia la portada: una imagen en el cuerpo, o ?isbn= para usar la
// portada encontrada para ese ISBN (el servidor la busca otra vez: nunca se
// descarga una URL que mande el navegador).
func (a *api) putCover(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !b.CanEdit {
		a.fail(w, errForbidden)
		return
	}
	var data []byte
	if q := r.URL.Query().Get("isbn"); q != "" {
		isbn, ok := normalizeISBN(q)
		if !ok {
			a.fail(w, bad("el ISBN no es válido"))
			return
		}
		info, err := a.catalog.lookupISBN(r.Context(), isbn)
		if err != nil || info.coverURL == "" {
			a.fail(w, bad("no hay portada para ese ISBN"))
			return
		}
		if data, _, err = a.catalog.fetchImage(r.Context(), info.coverURL); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("no se pudo descargar la portada: %s", err)})
			return
		}
	} else {
		data, err = io.ReadAll(io.LimitReader(r.Body, maxCoverBytes+1))
		if err != nil || len(data) > maxCoverBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
			a.fail(w, bad("la portada debe ser una imagen de 5 MB como máximo"))
			return
		}
	}
	if err := a.files.saveCover(b, bytes.NewReader(data)); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.store.coverChanged(r.Context(), b.SHA256); err != nil {
		a.fail(w, err)
		return
	}
	b, err = a.store.get(r.Context(), who, b.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}
