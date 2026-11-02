// Package seal cifra los datos personales que las apps guardan en la base de
// datos y calcula huellas de sus palabras para poder buscar sin guardar el
// texto.
//
// Cada app tiene su clave (DATA_DIR/clave-datos, 32 bytes aleatorios que se
// crean la primera vez). De ella salen dos subclaves: una para cifrar con
// AES-256-GCM y otra para las huellas (HMAC-SHA256). Un volcado de la base,
// una copia o una consulta indebida solo ven bytes cifrados y números.
//
// Límite: la clave vive en el mismo móvil, con los mismos permisos que la
// app. Quien controle el móvil puede leerla; para eso está el cifrado del
// propio Android. Las copias nocturnas llevan la clave dentro del tar cifrado
// con age: sin ellas, perder la clave es perder los datos.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// KeyFile es el nombre del fichero de la clave dentro de DATA_DIR.
const KeyFile = "clave-datos"

const version = 1

// Box cifra y descifra con la clave de una app.
type Box struct {
	aead cipher.AEAD
	mac  []byte
}

// Load lee dir/clave-datos o la crea. Sin dir (tests y desarrollo) la clave
// solo dura lo que el proceso.
func Load(dir string) (*Box, error) {
	key := make([]byte, 32)
	if dir == "" {
		rand.Read(key)
		return New(key)
	}
	path := filepath.Join(dir, KeyFile)
	b, err := os.ReadFile(path)
	switch {
	case err == nil && len(b) == 32:
		return New(b)
	case err == nil:
		return nil, errors.New("la clave de datos (" + path + ") está dañada: restáurala de una copia")
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	rand.Read(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: si otra instancia la acaba de crear, se usa la suya.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return Load(dir)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return New(key)
}

// New crea una Box con una clave de 32 bytes.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("la clave debe tener 32 bytes")
	}
	block, err := aes.NewCipher(derive(key, "cifrado"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, mac: derive(key, "huellas")}, nil
}

func derive(key []byte, label string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("homeserver/seal/" + label))
	return m.Sum(nil)
}

// Seal cifra plain. what (tabla y columna, por ejemplo "agenda/objects")
// queda ligado al cifrado: lo de una columna no se descifra como otra.
func (b *Box) Seal(what string, plain []byte) []byte {
	out := make([]byte, 1, 1+b.aead.NonceSize()+len(plain)+b.aead.Overhead())
	out[0] = version
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plain, []byte(what))
}

// Open descifra lo que hizo Seal con el mismo what.
func (b *Box) Open(what string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != version {
		return nil, errors.New("dato cifrado dañado")
	}
	plain, err := b.aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte(what))
	if err != nil {
		return nil, errors.New("no se puede descifrar: la clave de datos no es la que cifró esto")
	}
	return plain, nil
}

func (b *Box) SealString(what, s string) []byte { return b.Seal(what, []byte(s)) }

func (b *Box) OpenString(what string, sealed []byte) (string, error) {
	p, err := b.Open(what, sealed)
	return string(p), err
}

// SealJSON cifra v pasado a JSON.
func (b *Box) SealJSON(what string, v any) ([]byte, error) {
	p, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b.Seal(what, p), nil
}

func (b *Box) OpenJSON(what string, sealed []byte, v any) error {
	p, err := b.Open(what, sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal(p, v)
}

// ---- Huellas para buscar ----

const (
	prefixMin = 3    // desde 3 letras, se encuentra por el principio de la palabra
	prefixMax = 10   // más allá, basta con las 10 primeras
	maxTokens = 6000 // por fila, para que un correo enorme no pese de más
)

// Token es la huella de un valor exacto (por ejemplo, "MM-DD" de un
// cumpleaños). field separa espacios: la misma palabra en el remitente y en
// el texto da huellas distintas.
func (b *Box) Token(field, value string) int64 {
	m := hmac.New(sha256.New, b.mac)
	m.Write([]byte(field))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return int64(binary.BigEndian.Uint64(m.Sum(nil)))
}

// Index devuelve las huellas de las palabras de text: de cada una, la
// palabra entera y sus comienzos de 3 a 10 letras (sin tildes ni mayúsculas).
// Se añaden a las que ya haya en into, sin repetir.
func (b *Box) Index(into []int64, field, text string) []int64 {
	seen := make(map[int64]bool, len(into))
	for _, t := range into {
		seen[t] = true
	}
	add := func(s string) bool {
		if len(into) >= maxTokens {
			return false
		}
		t := b.Token(field, s)
		if !seen[t] {
			seen[t] = true
			into = append(into, t)
		}
		return true
	}
	for _, w := range Words(text) {
		r := []rune(w)
		for n := 1; n <= min(len(r), prefixMax); n++ {
			if (n >= prefixMin || n == len(r)) && !add(string(r[:n])) {
				return into
			}
		}
	}
	return into
}

// Query devuelve las huellas que debe tener una fila para contener todas
// las palabras de q (cada una, entera o como comienzo de palabra si tiene
// al menos 3 letras). Sin palabras, una lista vacía (no nil): en SQL,
// «tokens @> '{}'» lo cumple todo.
func (b *Box) Query(field, q string) []int64 {
	out := []int64{}
	for _, w := range Words(q) {
		r := []rune(w)
		if len(r) > prefixMax {
			r = r[:prefixMax]
		}
		out = append(out, b.Token(field, string(r)))
	}
	return out
}

// Fold pasa a minúsculas y quita las tildes.
func Fold(s string) string {
	var sb strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Words parte s en palabras (letras y números), ya plegadas con Fold.
func Words(s string) []string {
	return strings.FieldsFunc(Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}
