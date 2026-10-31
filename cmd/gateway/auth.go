package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Parámetros de argon2id recomendados por OWASP (19 MiB, 2 pasadas).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// hashPassword devuelve el formato estándar $argon2id$v=19$m=…,t=…,p=…$sal$hash.
func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key))
}

func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// sessions firma cookies "caducidad.firma" con HMAC-SHA256. No guarda estado:
// cambiar la clave cierra todas las sesiones.
type sessions struct {
	key []byte
	ttl time.Duration
}

func (s sessions) issue(now time.Time) string {
	exp := strconv.FormatInt(now.Add(s.ttl).Unix(), 10)
	return exp + "." + s.sign(exp)
}

func (s sessions) valid(cookie string, now time.Time) bool {
	exp, sig, ok := strings.Cut(cookie, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(exp))) {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && now.Unix() < t
}

func (s sessions) sign(msg string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("session:" + msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// loadKey lee la clave de sesiones o la crea la primera vez.
func loadKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	key := make([]byte, 32)
	rand.Read(key)
	return key, os.WriteFile(path, key, 0o600)
}

// limiter cuenta intentos de login fallidos por IP en una ventana deslizante.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) allowed(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip, now)) < l.max
}

func (l *limiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.recent(ip, now), now)
}

func (l *limiter) recent(ip string, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range l.fails[ip] {
		if now.Sub(t) < l.window {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		delete(l.fails, ip)
	}
	return out
}

// tokens son credenciales para el agente y los clientes MCP. En disco solo se
// guarda su SHA-256.
type tokenStore struct {
	path string
}

type tokenEntry struct {
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
}

func (s tokenStore) load() (map[string]tokenEntry, error) {
	m := map[string]tokenEntry{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s tokenStore) save(m map[string]tokenEntry) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// create genera un token nuevo y devuelve su valor, que no se vuelve a mostrar.
func (s tokenStore) create(name string) (string, error) {
	m, err := s.load()
	if err != nil {
		return "", err
	}
	if _, dup := m[name]; dup {
		return "", fmt.Errorf("ya existe un token %q", name)
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := "hs_" + base64.RawURLEncoding.EncodeToString(b)
	m[name] = tokenEntry{Hash: hashToken(tok), Created: time.Now().UTC()}
	return tok, s.save(m)
}

func (s tokenStore) revoke(name string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return fmt.Errorf("no existe el token %q", name)
	}
	delete(m, name)
	return s.save(m)
}

// check devuelve el nombre del token si es válido.
func (s tokenStore) check(tok string) (string, bool) {
	m, err := s.load()
	if err != nil {
		return "", false
	}
	h := hashToken(tok)
	for name, e := range m {
		if subtle.ConstantTimeCompare([]byte(e.Hash), []byte(h)) == 1 {
			return name, true
		}
	}
	return "", false
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
