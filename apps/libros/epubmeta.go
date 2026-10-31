package main

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"path"
	"strings"
)

// Lectura mínima de los metadatos de un EPUB en el servidor, para los libros
// importados (los subidos a mano los extrae el navegador con foliate-js).

type epubMeta struct {
	Title       string
	Authors     []string
	Language    string
	Subjects    []string
	Description string
	cover       []byte
}

type opfPackage struct {
	Metadata struct {
		Titles      []string `xml:"title"`
		Creators    []string `xml:"creator"`
		Languages   []string `xml:"language"`
		Subjects    []string `xml:"subject"`
		Description string   `xml:"description"`
		Metas       []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
}

// readEPUBMeta devuelve lo que pueda leer; un EPUB raro no es un error.
func readEPUBMeta(file string) epubMeta {
	var m epubMeta
	z, err := zip.OpenReader(file)
	if err != nil {
		return m
	}
	defer z.Close()
	read := func(name string, max int64) []byte {
		for _, f := range z.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil
				}
				defer rc.Close()
				b, _ := io.ReadAll(io.LimitReader(rc, max))
				return b
			}
		}
		return nil
	}
	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(read("META-INF/container.xml", 64<<10), &container) != nil || len(container.Rootfiles) == 0 {
		return m
	}
	opfPath := container.Rootfiles[0].FullPath
	var opf opfPackage
	if xml.Unmarshal(read(opfPath, 1<<20), &opf) != nil {
		return m
	}
	md := opf.Metadata
	if len(md.Titles) > 0 {
		m.Title = md.Titles[0]
	}
	m.Authors = md.Creators
	if len(md.Languages) > 0 {
		m.Language = md.Languages[0]
	}
	m.Subjects = md.Subjects
	m.Description = md.Description

	// Portada: EPUB 3 (properties="cover-image") o EPUB 2 (<meta name="cover">).
	coverID := ""
	for _, meta := range md.Metas {
		if meta.Name == "cover" {
			coverID = meta.Content
		}
	}
	for _, it := range opf.Manifest.Items {
		if strings.Contains(it.Properties, "cover-image") || (coverID != "" && it.ID == coverID) {
			if strings.HasPrefix(it.MediaType, "image/") {
				m.cover = read(path.Join(path.Dir(opfPath), it.Href), maxCoverBytes)
				break
			}
		}
	}
	return m
}
