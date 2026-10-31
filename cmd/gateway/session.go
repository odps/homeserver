package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"
)

// Las sesiones viven en el servidor: la cookie solo lleva un identificador
// aleatorio y en disco se guarda su hash. Así se pueden listar y cerrar.
// Panel: 30 días como máximo y 7 sin uso. Apps: sin máximo y 90 días sin uso
// (instaladas como app no vuelven a pedir la contraseña mientras se usen).
const (
	sessionTTL     = 30 * 24 * time.Hour
	sessionIdle    = 7 * 24 * time.Hour
	appSessionIdle = 90 * 24 * time.Hour
)

type session struct {
	ID       string    `json:"id"`             // identificador corto para el panel (no es la cookie)
	User     string    `json:"user,omitempty"` // cuenta de app; vacío en las del panel
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	IP       string    `json:"ip"`
	Agent    string    `json:"agent"`
	CSRF     string    `json:"csrf"`
}

type sessionStore struct {
	path      string
	ttl       time.Duration // máximo absoluto; 0 = sin máximo
	idle      time.Duration
	mu        sync.Mutex
	byHash    map[string]*session
	lastFlush time.Time
}

func newSessionStore(path string, ttl, idle time.Duration) (*sessionStore, error) {
	s := &sessionStore{path: path, ttl: ttl, idle: idle, byHash: map[string]*session{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.byHash)
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSession(cookie string) string {
	sum := sha256.Sum256([]byte("session:" + cookie))
	return hex.EncodeToString(sum[:])
}

// create devuelve el valor de la cookie de una sesión nueva.
func (s *sessionStore) create(user, ip, agent string, now time.Time) (string, *session, error) {
	cookie := randomToken(32)
	sess := &session{ID: randomToken(6), User: user, Created: now, LastSeen: now, IP: ip, Agent: truncate(agent, 160), CSRF: randomToken(24)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byHash[hashSession(cookie)] = sess
	return cookie, sess, s.flushLocked(now)
}

// get valida la cookie y actualiza el último uso.
func (s *sessionStore) get(cookie, ip string, now time.Time) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashSession(cookie)
	sess, ok := s.byHash[h]
	if !ok {
		return nil, false
	}
	if s.expired(sess, now) {
		delete(s.byHash, h)
		s.flushLocked(now)
		return nil, false
	}
	sess.LastSeen, sess.IP = now, ip
	// No escribe en disco en cada petición: como mucho una vez por minuto.
	if now.Sub(s.lastFlush) > time.Minute {
		s.flushLocked(now)
	}
	copy := *sess
	return &copy, true
}

func (s *sessionStore) list(now time.Time) []session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []session
	for _, sess := range s.byHash {
		if !s.expired(sess, now) {
			out = append(out, *sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (s *sessionStore) expired(sess *session, now time.Time) bool {
	return (s.ttl > 0 && now.Sub(sess.Created) > s.ttl) || now.Sub(sess.LastSeen) > s.idle
}

// revokeUser cierra todas las sesiones de una cuenta de app.
func (s *sessionStore) revokeUser(user string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for h, sess := range s.byHash {
		if sess.User == user {
			delete(s.byHash, h)
			n++
		}
	}
	s.flushLocked(now)
	return n
}

// revoke cierra la sesión con ese ID corto; con "" las cierra todas.
func (s *sessionStore) revoke(id string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for h, sess := range s.byHash {
		if id == "" || sess.ID == id {
			delete(s.byHash, h)
			n++
		}
	}
	s.flushLocked(now)
	return n
}

func (s *sessionStore) revokeCookie(cookie string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byHash, hashSession(cookie))
	s.flushLocked(now)
}

func (s *sessionStore) flushLocked(now time.Time) error {
	s.lastFlush = now
	b, err := json.Marshal(s.byHash)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
