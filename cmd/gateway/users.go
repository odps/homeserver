package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cuentas de las apps: separadas del administrador (contraseña + TOTP del
// panel). Entran con usuario y contraseña y solo ven las apps que tengan
// permitidas. En disco solo se guarda el hash argon2id.

type appUser struct {
	Name      string     `json:"name"` // nombre visible
	Hash      string     `json:"hash"`
	Apps      []string   `json:"apps"`
	Role      string     `json:"role"` // admin o usuario (dentro de las apps)
	Disabled  bool       `json:"disabled,omitempty"`
	Created   time.Time  `json:"created"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

func (u appUser) allows(app string) bool { return !u.Disabled && slices.Contains(u.Apps, app) }

var validUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

const minAppPassword = 10

type userStore struct {
	path string
	mu   sync.Mutex
}

func (s *userStore) load() (map[string]appUser, error) {
	m := map[string]appUser{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *userStore) save(m map[string]appUser) error {
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

type userView struct {
	Username string
	appUser
}

func (s *userStore) list() ([]userView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]userView, 0, len(m))
	for k, u := range m {
		out = append(out, userView{k, u})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (s *userStore) get(username string) (appUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return appUser{}, false
	}
	u, ok := m[username]
	return u, ok
}

func cleanApps(apps []string) []string {
	out := []string{}
	for _, a := range apps {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// create da de alta una cuenta con una contraseña temporal aleatoria, que se
// devuelve para enseñarla una sola vez.
func (s *userStore) create(username, name string, apps []string, role string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername.MatchString(username) {
		return "", errors.New("usuario no válido: 2-32 caracteres en minúscula, números, punto, guion o guion bajo")
	}
	if role != "admin" && role != "usuario" {
		return "", errors.New("rol no válido")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = username
	}
	pass := tempPassword()
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", err
	}
	if _, dup := m[username]; dup {
		return "", fmt.Errorf("ya existe el usuario %q", username)
	}
	m[username] = appUser{Name: truncate(name, 60), Hash: hashPassword(pass), Apps: cleanApps(apps), Role: role, Created: time.Now().UTC()}
	return pass, s.save(m)
}

func (s *userStore) update(username string, fn func(*appUser) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	u, ok := m[username]
	if !ok {
		return fmt.Errorf("no existe el usuario %q", username)
	}
	if err := fn(&u); err != nil {
		return err
	}
	m[username] = u
	return s.save(m)
}

func (s *userStore) delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[username]; !ok {
		return fmt.Errorf("no existe el usuario %q", username)
	}
	delete(m, username)
	return s.save(m)
}

// resetPassword pone una contraseña temporal nueva y la devuelve.
func (s *userStore) resetPassword(username string) (string, error) {
	pass := tempPassword()
	return pass, s.update(username, func(u *appUser) error { u.Hash = hashPassword(pass); return nil })
}

func (s *userStore) setPassword(username, pass string) error {
	if len([]rune(pass)) < minAppPassword {
		return fmt.Errorf("la contraseña debe tener al menos %d caracteres", minAppPassword)
	}
	return s.update(username, func(u *appUser) error { u.Hash = hashPassword(pass); return nil })
}

// check valida usuario y contraseña. Con un usuario inexistente también
// calcula argon2, para no revelar por el tiempo de respuesta qué cuentas existen.
func (s *userStore) check(ctx context.Context, username, pass string) (appUser, bool, error) {
	u, ok := s.get(username)
	hash := u.Hash
	if !ok {
		hash = dummyHash
	}
	valid, err := checkPassword(ctx, hash, pass)
	if err != nil {
		return u, false, err
	}
	return u, ok && valid && !u.Disabled, nil
}

var dummyHash = hashPassword("contraseña que no usa nadie")

func (s *userStore) touchLogin(username string, now time.Time) {
	s.update(username, func(u *appUser) error { t := now.UTC(); u.LastLogin = &t; return nil })
}

// tempPassword genera una contraseña legible de 4 grupos (unos 80 bits).
func tempPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 16)
	rand.Read(b)
	var out strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(b[i])%len(alphabet)])
	}
	return out.String()
}
