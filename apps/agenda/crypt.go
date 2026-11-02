package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

// sealer cifra las credenciales del correo (contraseñas de aplicación y
// tokens de Outlook) con una clave que vive fuera de la base de datos, en la
// carpeta de datos de la app: un volcado de la base no las revela.
type sealer struct {
	aead cipher.AEAD
}

// loadSealer lee la clave de dir/clave-correo o la crea. Sin dir (tests), la
// clave solo dura lo que el proceso.
func loadSealer(dir string) (*sealer, error) {
	key := make([]byte, 32)
	if dir == "" {
		rand.Read(key)
	} else {
		path := filepath.Join(dir, "clave-correo")
		b, err := os.ReadFile(path)
		switch {
		case err == nil && len(b) == 32:
			key = b
		case err == nil || errors.Is(err, os.ErrNotExist):
			if err == nil {
				return nil, errors.New("la clave del correo (" + path + ") está dañada")
			}
			rand.Read(key)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, key, 0o600); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(plain string) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, []byte(plain), nil)
}

func (s *sealer) open(b []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(b) < n {
		return "", errors.New("credencial dañada")
	}
	plain, err := s.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return "", errors.New("no se puede descifrar la credencial: vuelve a conectar la cuenta")
	}
	return string(plain), nil
}
