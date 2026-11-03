package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Catálogos abiertos de ejercicios, copiados en datos/externos.json.gz (ver
// generar_catalogo.go): wger (CC-BY-SA, con español) y free-exercise-db
// (dominio público). Los ejercicios del catálogo propio apuntan a ellos (Wger,
// Fedb) para la ficha; los de wger que no están en él se añaden con la clave
// «w<id>».

//go:embed datos/externos.json.gz
var externosGz []byte

type extText struct {
	Name    string   `json:"name"`
	Desc    string   `json:"desc"`
	Aliases []string `json:"aliases"`
	Author  string   `json:"author"`
}

type extImage struct {
	URL    string `json:"url"`
	Author string `json:"author"`
	Main   bool   `json:"main"`
}

type wgerEx struct {
	ID        int        `json:"id"`
	Category  string     `json:"cat"`
	ES        *extText   `json:"es"`
	EN        *extText   `json:"en"`
	Muscles   []string   `json:"muscles"`
	Secondary []string   `json:"muscles2"`
	Equipment []string   `json:"equip"`
	Images    []extImage `json:"images"`
	License   string     `json:"license"`
}

type fedbEx struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Category  string   `json:"cat"`
	Equipment string   `json:"equip"`
	Primary   []string `json:"muscles"`
	Secondary []string `json:"muscles2"`
	Steps     []string `json:"steps"`
	Images    []string `json:"images"`
}

const fedbImages = "https://raw.githubusercontent.com/yuhonas/free-exercise-db/main/exercises/"

var ext struct {
	once     sync.Once
	wger     map[int]*wgerEx
	fedb     map[string]*fedbEx
	extra    []exercise          // de wger, que no están en el catálogo propio
	extraKey map[string]exercise // «w<id>» → ejercicio
}

func loadExternal() {
	ext.once.Do(func() {
		ext.wger, ext.fedb, ext.extraKey = map[int]*wgerEx{}, map[string]*fedbEx{}, map[string]exercise{}
		zr, err := gzip.NewReader(bytes.NewReader(externosGz))
		if err != nil {
			panic("datos/externos.json.gz: " + err.Error())
		}
		var snap struct {
			Wger []*wgerEx `json:"wger"`
			Fedb []*fedbEx `json:"fedb"`
		}
		if err := json.NewDecoder(zr).Decode(&snap); err != nil {
			panic("datos/externos.json.gz: " + err.Error())
		}
		for _, w := range snap.Wger {
			ext.wger[w.ID] = w
		}
		for _, f := range snap.Fedb {
			ext.fedb[f.ID] = f
		}
		mapped := map[int]bool{}
		for _, e := range catalog {
			mapped[e.Wger] = true
		}
		for _, w := range snap.Wger {
			if mapped[w.ID] {
				continue
			}
			e := exercise{Key: "w" + strconv.Itoa(w.ID), Kind: "reps", Equipment: "otro", Wger: w.ID}
			if w.ES != nil {
				e.Name = w.ES.Name
				e.Aliases = append(e.Aliases, w.ES.Aliases...)
			}
			if w.EN != nil {
				if e.Name == "" {
					e.Name = w.EN.Name
				}
				e.Aliases = append(e.Aliases, w.EN.Name)
				e.Aliases = append(e.Aliases, w.EN.Aliases...)
			}
			e.Muscles = groupsOf(w.Muscles)
			e.Secondary = groupsOf(w.Secondary)
			ext.extra = append(ext.extra, e)
			ext.extraKey[e.Key] = e
		}
	})
}

// lookup da un ejercicio del catálogo propio o de los añadidos de wger.
func lookup(key string) (exercise, bool) {
	if e, ok := byKey(key); ok {
		return e, true
	}
	loadExternal()
	e, ok := ext.extraKey[key]
	return e, ok
}

// allExercises son el catálogo propio y, detrás, los añadidos de wger.
func allExercises() []exercise {
	loadExternal()
	return append(append([]exercise{}, catalog...), ext.extra...)
}

// Músculos de wger y de free-exercise-db en los grupos de la app.
var muscleMap = map[string]string{
	"abs": "abdomen", "abdominals": "abdomen", "obliquus externus abdominis": "abdomen", "serratus anterior": "pecho",
	"biceps": "biceps", "brachialis": "biceps", "shoulders": "hombro", "chest": "pecho", "lats": "espalda",
	"middle back": "espalda", "lower back": "lumbar", "quads": "cuadriceps", "quadriceps": "cuadriceps", "glutes": "gluteo",
	"hamstrings": "isquios", "calves": "gemelo", "soleus": "gemelo", "triceps": "triceps", "trapezius": "trapecio",
	"traps": "trapecio", "forearms": "antebrazo", "adductors": "aductores", "abductors": "gluteo", "neck": "trapecio",
}

func groupsOf(names []string) []string {
	var out []string
	for _, n := range names {
		if g, ok := muscleMap[strings.ToLower(n)]; ok && !contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// imageRef es una imagen de la ficha, con su origen para citarlo.
type imageRef struct {
	URL    string // remota; la web la sirve desde su copia en el móvil
	Credit string // «wger.de · autor (CC-BY-SA 3)», «free-exercise-db (dominio público)»
}

// exDetail es la ficha de un ejercicio.
type exDetail struct {
	Name      string
	English   string   // nombre en inglés, si lo hay
	Desc      string   // explicación (español si la hay; si no, inglés)
	DescLang  string   // "es" o "en"
	Muscles   []string // principales y secundarios en palabras
	Equipment []string
	Images    []imageRef
	Sources   []string // de dónde sale lo que se enseña, para citarlo
}

// details arma la ficha con wger (preferido: suele estar en español) y
// free-exercise-db (imágenes y pasos si wger no los tiene).
func details(e exercise) exDetail {
	loadExternal()
	d := exDetail{Name: e.Name, Muscles: append(append([]string{}, e.Muscles...), e.Secondary...)}
	if w := ext.wger[e.Wger]; w != nil {
		switch {
		case w.ES != nil && w.ES.Desc != "":
			d.Desc, d.DescLang = w.ES.Desc, "es"
		case w.EN != nil && w.EN.Desc != "":
			d.Desc, d.DescLang = w.EN.Desc, "en"
		}
		if w.EN != nil {
			d.English = w.EN.Name
		}
		d.Equipment = w.Equipment
		for _, im := range w.Images {
			credit := "wger.de"
			if im.Author != "" {
				credit += " · " + im.Author
			}
			d.Images = append(d.Images, imageRef{URL: im.URL, Credit: credit + " (" + orText(w.License, "CC-BY-SA") + ")"})
		}
		d.Sources = append(d.Sources, fmt.Sprintf("wger.de, ejercicio %d (%s)", w.ID, orText(w.License, "CC-BY-SA")))
	}
	if f := ext.fedb[e.Fedb]; f != nil {
		if d.Desc == "" && len(f.Steps) > 0 {
			d.Desc, d.DescLang = "• "+strings.Join(f.Steps, "\n• "), "en"
		}
		if d.English == "" {
			d.English = f.Name
		}
		if len(d.Images) == 0 {
			for _, p := range f.Images {
				d.Images = append(d.Images, imageRef{URL: fedbImages + p, Credit: "free-exercise-db (dominio público)"})
			}
		}
		if len(d.Equipment) == 0 && f.Equipment != "" {
			d.Equipment = []string{f.Equipment}
		}
		d.Sources = append(d.Sources, "free-exercise-db: "+f.ID+" (dominio público)")
	}
	return d
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
