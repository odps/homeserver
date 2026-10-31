package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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

// argonSlots limita los cálculos simultáneos: cada uno reserva 19 MiB y una
// avalancha de intentos agotaría la RAM del móvil.
var argonSlots = make(chan struct{}, 2)

// hashPassword devuelve el formato estándar $argon2id$v=19$m=…,t=…,p=…$sal$hash.
func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key))
}

var errBusy = errors.New("demasiados intentos simultáneos")

// checkPassword espera turno (hasta que venza ctx) antes de calcular argon2.
func checkPassword(ctx context.Context, encoded, password string) (bool, error) {
	select {
	case argonSlots <- struct{}{}:
		defer func() { <-argonSlots }()
	case <-ctx.Done():
		return false, errBusy
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, nil
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, nil
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false, nil
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// limiter cuenta intentos fallidos por clave (IP, o "*" para el total) en una
// ventana deslizante.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) allowed(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, now)) < l.max
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key, now), now)
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		delete(l.fails, key)
	}
	return out
}

// tokens son credenciales para el agente y los clientes MCP. En disco solo se
// guarda su SHA-256. Cada token vale para unas apps concretas ("*" = todas) y
// puede caducar. No dan acceso al panel.
type tokenStore struct {
	path string
	mu   sync.Mutex
}

type tokenEntry struct {
	Hash     string     `json:"hash"`
	User     string     `json:"user,omitempty"` // cuenta de app en cuyo nombre actúa (opcional)
	Created  time.Time  `json:"created"`
	Apps     []string   `json:"apps"`
	Expires  *time.Time `json:"expires,omitempty"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

func (e tokenEntry) allows(app string, now time.Time) bool {
	if e.Expires != nil && now.After(*e.Expires) {
		return false
	}
	return slices.Contains(e.Apps, "*") || slices.Contains(e.Apps, app)
}

func (s *tokenStore) load() (map[string]tokenEntry, error) {
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

func (s *tokenStore) save(m map[string]tokenEntry) error {
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

func (s *tokenStore) list() (map[string]tokenEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// create genera un token nuevo y devuelve su valor, que no se vuelve a mostrar.
func (s *tokenStore) create(name, user string, apps []string, ttl time.Duration) (string, error) {
	if !validTokenName(name) {
		return "", errors.New("nombre de token no válido (letras, números, - y _)")
	}
	if len(apps) == 0 {
		return "", errors.New("indica al menos una app (o * para todas)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", err
	}
	if _, dup := m[name]; dup {
		return "", fmt.Errorf("ya existe un token %q", name)
	}
	tok := "hs_" + randomToken(32)
	e := tokenEntry{Hash: hashToken(tok), User: user, Created: time.Now().UTC(), Apps: apps}
	if ttl > 0 {
		exp := e.Created.Add(ttl)
		e.Expires = &exp
	}
	m[name] = e
	return tok, s.save(m)
}

func (s *tokenStore) revoke(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// check devuelve el nombre y los datos del token si es válido para la app.
func (s *tokenStore) check(tok, app string, now time.Time) (string, tokenEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", tokenEntry{}, false
	}
	h := hashToken(tok)
	for name, e := range m {
		if subtle.ConstantTimeCompare([]byte(e.Hash), []byte(h)) == 1 {
			if !e.allows(app, now) {
				return name, e, false
			}
			// Registra el último uso como mucho una vez por hora.
			if e.LastUsed == nil || now.Sub(*e.LastUsed) > time.Hour {
				t := now.UTC()
				e.LastUsed = &t
				m[name] = e
				s.save(m)
			}
			return name, e, true
		}
	}
	return "", tokenEntry{}, false
}

func validTokenName(n string) bool {
	if n == "" || len(n) > 40 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
