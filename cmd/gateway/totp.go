package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

// TOTP según RFC 6238: HMAC-SHA1, pasos de 30 s, 6 dígitos. Se acepta un paso
// de margen a cada lado y nunca el mismo paso dos veces (anti-repetición).
const totpStep = 30

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

func totpCode(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

type totpVerifier struct {
	secret   []byte
	mu       sync.Mutex
	lastStep uint64
}

func newTOTPVerifier(secretB32 string) (*totpVerifier, error) {
	s, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secretB32)))
	if err != nil || len(s) < 10 {
		return nil, errors.New("GATEWAY_TOTP_SECRET no válido")
	}
	return &totpVerifier{secret: s}, nil
}

func (v *totpVerifier) verify(code string, now time.Time) bool {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != 6 {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	step := uint64(now.Unix() / totpStep)
	for _, s := range []uint64{step - 1, step, step + 1} {
		if s <= v.lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(v.secret, s)), []byte(code)) == 1 {
			v.lastStep = s
			return true
		}
	}
	return false
}

func otpauthURI(secret string) string {
	q := url.Values{"secret": {secret}, "issuer": {"homeserver"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/homeserver:propietario?" + q.Encode()
}

// qrText dibuja el QR con medios bloques Unicode (dos filas por línea), con
// margen blanco para que los lectores lo detecten en terminales oscuras.
func qrText(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2
	black := func(x, y int) bool {
		if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
			return false
		}
		return c.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		for x := -quiet; x < c.Size+quiet; x++ {
			top, bottom := black(x, y), black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bottom:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// recoveryStore guarda los hashes de los códigos de recuperación, de un solo
// uso, por si se pierde el móvil con la app de TOTP.
type recoveryStore struct {
	path string
	mu   sync.Mutex
}

func (s *recoveryStore) generate(n int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	codes := make([]string, n)
	hashes := make([]string, n)
	for i := range codes {
		b := make([]byte, 5)
		rand.Read(b)
		c := strings.ToLower(b32.EncodeToString(b)) // 8 caracteres
		codes[i] = c[:4] + "-" + c[4:]
		hashes[i] = hashRecovery(codes[i])
	}
	return codes, s.save(hashes)
}

// use consume el código si es válido.
func (s *recoveryStore) use(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hashes, err := s.load()
	if err != nil {
		return false
	}
	h := hashRecovery(code)
	for i, stored := range hashes {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
			hashes = append(hashes[:i], hashes[i+1:]...)
			return s.save(hashes) == nil
		}
	}
	return false
}

func (s *recoveryStore) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, _ := s.load()
	return len(h)
}

func (s *recoveryStore) load() ([]string, error) {
	var h []string
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return h, json.Unmarshal(b, &h)
}

func (s *recoveryStore) save(h []string) error {
	b, _ := json.Marshal(h)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashRecovery(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	sum := sha256.Sum256([]byte("recovery:" + code))
	return hex.EncodeToString(sum[:])
}
