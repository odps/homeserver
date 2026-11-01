// copias comprueba y descifra en el PC una copia de seguridad del homeserver
// (backups/copias/*.tar.gz.age). Necesita la clave privada que dio el panel
// al crear la clave de las copias (AGE-SECRET-KEY-1…), guardada en un fichero.
//
//	go run ./cmd/copias -clave clave.txt copia.tar.gz.age            comprueba y lista
//	go run ./cmd/copias -clave clave.txt -extraer dir copia.tar.gz.age  la descifra en dir
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

func main() {
	keyFile := flag.String("clave", "", "fichero con la clave privada (AGE-SECRET-KEY-1…)")
	outDir := flag.String("extraer", "", "directorio donde descifrar la copia (vacío: solo comprobar)")
	flag.Parse()
	if *keyFile == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "uso: copias -clave clave.txt [-extraer dir] copia.tar.gz.age")
		os.Exit(2)
	}
	n, size, err := run(*keyFile, flag.Arg(0), *outDir, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("Copia correcta: %d ficheros, %.1f MB sin comprimir.\n", n, float64(size)/(1<<20))
}

func run(keyFile, archive, outDir string, log io.Writer) (int, int64, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return 0, 0, err
	}
	ids, err := age.ParseIdentities(bytes.NewReader(b))
	if err != nil {
		return 0, 0, fmt.Errorf("la clave no es válida: %w", err)
	}
	f, err := os.Open(archive)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	dec, err := age.Decrypt(f, ids...)
	if err != nil {
		return 0, 0, fmt.Errorf("no se puede descifrar (¿otra clave?): %w", err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return 0, 0, err
	}
	tr := tar.NewReader(gz)
	var n int
	var total int64
	dumpOK := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, total, fmt.Errorf("copia dañada: %w", err)
		}
		n++
		total += h.Size
		var w io.Writer = io.Discard
		var out *os.File
		if outDir != "" {
			dst, err := safeJoin(outDir, h.Name)
			if err != nil {
				return n, total, err
			}
			os.MkdirAll(filepath.Dir(dst), 0o700)
			if out, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600); err != nil {
				return n, total, err
			}
			w = out
		}
		head := &bytes.Buffer{}
		_, err = io.Copy(io.MultiWriter(w, &limited{head, 5}), tr)
		if out != nil {
			out.Close()
		}
		if err != nil {
			return n, total, fmt.Errorf("copia dañada en %s: %w", h.Name, err)
		}
		if h.Name == "homeserver.dump" {
			dumpOK = head.String() == "PGDMP" // cabecera de pg_dump --format=custom
		}
		if h.Name == "LEEME.txt" || strings.Count(h.Name, "/") <= 1 {
			fmt.Fprintf(log, "  %s (%d bytes)\n", h.Name, h.Size)
		}
	}
	if !dumpOK {
		return n, total, errors.New("falta la base de datos (homeserver.dump) o no es un pg_dump")
	}
	return n, total, nil
}

// safeJoin impide que un nombre del tar escriba fuera del directorio.
func safeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, filepath.FromSlash(name))
	if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(name) {
		return "", fmt.Errorf("nombre no permitido en la copia: %s", name)
	}
	return p, nil
}

// limited guarda solo los primeros n bytes.
type limited struct {
	buf *bytes.Buffer
	n   int
}

func (l *limited) Write(p []byte) (int, error) {
	if rest := l.n - l.buf.Len(); rest > 0 {
		l.buf.Write(p[:min(rest, len(p))])
	}
	return len(p), nil
}
