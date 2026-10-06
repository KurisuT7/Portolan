package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const keySize = 32

type Vault struct {
	aead cipher.AEAD
}

func New(encodedKey string) (*Vault, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("master key must decode to %d bytes", keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func GenerateKey() (string, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func (v *Vault) Seal(plaintext []byte, context string) (string, error) {
	if v == nil || v.aead == nil {
		return "", errors.New("vault is not initialized")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := v.aead.Seal(nonce, nonce, plaintext, []byte(context))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (v *Vault) Open(ciphertext, context string) ([]byte, error) {
	if v == nil || v.aead == nil {
		return nil, errors.New("vault is not initialized")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode sealed value: %w", err)
	}
	nonceSize := v.aead.NonceSize()
	if len(sealed) < nonceSize {
		return nil, errors.New("sealed value is too short")
	}
	plaintext, err := v.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(context))
	if err != nil {
		return nil, errors.New("sealed value authentication failed")
	}
	return plaintext, nil
}
