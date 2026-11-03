//go:build ignore

// Genera datos/externos.json.gz: una copia de los catálogos abiertos de
// ejercicios que usa la app, para no depender de ellos en el móvil.
//
//   - wger (https://wger.de, datos CC-BY-SA): nombre y explicación en español
//     e inglés, músculos, material e imágenes, con su autoría.
//   - free-exercise-db (https://github.com/yuhonas/free-exercise-db, dominio
//     público): explicación en inglés e imágenes.
//
// Uso (desde apps/entreno): go run generar_catalogo.go
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const userAgent = "homeserver-personal/1.0 (catálogo de ejercicios para uso personal)"

type text struct {
	Name    string   `json:"name"`
	Desc    string   `json:"desc,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
	Author  string   `json:"author,omitempty"`
}

type image struct {
	URL    string `json:"url"`
	Author string `json:"author,omitempty"`
	Main   bool   `json:"main,omitempty"`
}

type wgerEx struct {
	ID        int      `json:"id"`
	Category  string   `json:"cat,omitempty"`
	ES        *text    `json:"es,omitempty"`
	EN        *text    `json:"en,omitempty"`
	Muscles   []string `json:"muscles,omitempty"`
	Secondary []string `json:"muscles2,omitempty"`
	Equipment []string `json:"equip,omitempty"`
	Images    []image  `json:"images,omitempty"`
	License   string   `json:"license,omitempty"`
}

type fedbEx struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Category     string   `json:"cat,omitempty"`
	Equipment    string   `json:"equip,omitempty"`
	Level        string   `json:"level,omitempty"`
	Primary      []string `json:"muscles,omitempty"`
	Secondary    []string `json:"muscles2,omitempty"`
	Instructions []string `json:"steps,omitempty"`
	Images       []string `json:"images,omitempty"`
}

type snapshot struct {
	Generated string   `json:"generated"`
	Wger      []wgerEx `json:"wger"`
	Fedb      []fedbEx `json:"fedb"`
}

func get(url string, out any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

var (
	reItem  = regexp.MustCompile(`(?i)<li[^>]*>`)
	reBreak = regexp.MustCompile(`(?i)<(br|/p|/li|/ol|/ul|/h\d)[^>]*>`)
	reTag   = regexp.MustCompile(`<[^>]+>`)
	reBlank = regexp.MustCompile(`\n\s*\n+`)
)

// plain pasa la descripción en HTML a texto con viñetas.
func plain(s string) string {
	s = reItem.ReplaceAllString(s, "\n• ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = html.UnescapeString(reTag.ReplaceAllString(s, ""))
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" && l != "•" {
			lines = append(lines, l)
		}
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n"))
}

func main() {
	var snap snapshot
	snap.Generated = time.Now().Format("2006-01-02")

	type wgerPage struct {
		Next    string `json:"next"`
		Results []struct {
			ID       int `json:"id"`
			Category struct {
				Name string `json:"name"`
			} `json:"category"`
			Muscles []struct {
				Name   string `json:"name"`
				NameEN string `json:"name_en"`
			} `json:"muscles"`
			Secondary []struct {
				Name   string `json:"name"`
				NameEN string `json:"name_en"`
			} `json:"muscles_secondary"`
			Equipment []struct {
				Name string `json:"name"`
			} `json:"equipment"`
			License struct {
				ShortName string `json:"short_name"`
			} `json:"license"`
			LicenseAuthor string `json:"license_author"`
			Images        []struct {
				Image         string `json:"image"`
				IsMain        bool   `json:"is_main"`
				LicenseAuthor string `json:"license_author"`
			} `json:"images"`
			Translations []struct {
				Language      int    `json:"language"`
				Name          string `json:"name"`
				Description   string `json:"description"`
				LicenseAuthor string `json:"license_author"`
				Aliases       []struct {
					Alias string `json:"alias"`
				} `json:"aliases"`
			} `json:"translations"`
		} `json:"results"`
	}
	muscle := func(name, en string) string {
		if en != "" {
			return en
		}
		return name
	}
	for url := "https://wger.de/api/v2/exerciseinfo/?limit=100"; url != ""; {
		var p wgerPage
		if err := get(url, &p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, r := range p.Results {
			e := wgerEx{ID: r.ID, Category: r.Category.Name, License: r.License.ShortName}
			for _, m := range r.Muscles {
				e.Muscles = append(e.Muscles, muscle(m.Name, m.NameEN))
			}
			for _, m := range r.Secondary {
				e.Secondary = append(e.Secondary, muscle(m.Name, m.NameEN))
			}
			for _, q := range r.Equipment {
				e.Equipment = append(e.Equipment, q.Name)
			}
			for _, im := range r.Images {
				e.Images = append(e.Images, image{URL: im.Image, Author: im.LicenseAuthor, Main: im.IsMain})
			}
			for _, t := range r.Translations {
				if t.Language != 2 && t.Language != 4 { // inglés y español
					continue
				}
				x := &text{Name: strings.TrimSpace(t.Name), Desc: plain(t.Description), Author: t.LicenseAuthor}
				for _, a := range t.Aliases {
					if a := strings.TrimSpace(a.Alias); a != "" {
						x.Aliases = append(x.Aliases, a)
					}
				}
				if t.Language == 4 {
					e.ES = x
				} else {
					e.EN = x
				}
			}
			if e.ES == nil && e.EN == nil {
				continue
			}
			snap.Wger = append(snap.Wger, e)
		}
		url = p.Next
	}
	sort.Slice(snap.Wger, func(i, j int) bool { return snap.Wger[i].ID < snap.Wger[j].ID })

	var fedb []struct {
		ID               string   `json:"id"`
		Name             string   `json:"name"`
		Category         string   `json:"category"`
		Equipment        *string  `json:"equipment"`
		Level            string   `json:"level"`
		PrimaryMuscles   []string `json:"primaryMuscles"`
		SecondaryMuscles []string `json:"secondaryMuscles"`
		Instructions     []string `json:"instructions"`
		Images           []string `json:"images"`
	}
	if err := get("https://raw.githubusercontent.com/yuhonas/free-exercise-db/main/dist/exercises.json", &fedb); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range fedb {
		e := fedbEx{ID: f.ID, Name: f.Name, Category: f.Category, Level: f.Level, Primary: f.PrimaryMuscles,
			Secondary: f.SecondaryMuscles, Instructions: f.Instructions, Images: f.Images}
		if f.Equipment != nil {
			e.Equipment = *f.Equipment
		}
		snap.Fedb = append(snap.Fedb, e)
	}
	sort.Slice(snap.Fedb, func(i, j int) bool { return snap.Fedb[i].ID < snap.Fedb[j].ID })

	f, err := os.Create("datos/externos.json.gz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(snap); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	zw.Close()
	f.Close()
	es := 0
	for _, e := range snap.Wger {
		if e.ES != nil {
			es++
		}
	}
	fmt.Printf("wger: %d ejercicios (%d en español); free-exercise-db: %d\n", len(snap.Wger), es, len(snap.Fedb))
}
