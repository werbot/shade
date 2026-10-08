// Package crypt — the shade crypto layer: the key in a file, AES-256-GCM for values
// and a deterministic hash for looking up entities without decryption.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	keySize   = 32 // AES-256 key length
	nonceSize = 12 // standard GCM nonce length
	keyName   = "key"
)

// KeyPath returns the path to the key file in the home directory.
//
// Exported so that the file name is not repeated in the calling code: having diverged,
// a copy silently breaks checks like "the key does not exist yet" — for example, doctor would again start
// creating the very state it checks.
func KeyPath(home string) string { return filepath.Join(home, keyName) }

// LoadOrCreateKey reads the key from home/key, and if it is missing creates a file
// of 32 random bytes with mode 0600. The file is created with O_EXCL, so
// a concurrent process will not overwrite an already existing key.
func LoadOrCreateKey(home string) ([]byte, error) {
	path := KeyPath(home)
	key, err := readKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating the key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// another process created the key first — use it
		return readKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("creating key %s: %w", path, err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("writing key %s: %w", path, err)
	}
	return key, nil
}

// readKey reads the key from path and checks its length.
func readKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key %s: %w", path, err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("key %s: expected %d bytes, got %d", path, keySize, len(key))
	}
	return key, nil
}

// Encrypt encrypts plaintext with the key key (AES-256-GCM) and returns a blob
// of the form nonce || ciphertext || tag. Every call uses a fresh random
// nonce, so equal values give different blobs.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating the nonce: %w", err)
	}
	blob := make([]byte, 0, nonceSize+len(plaintext)+gcm.Overhead())
	blob = append(blob, nonce...)
	return gcm.Seal(blob, nonce, plaintext, nil), nil
}

// Decrypt decrypts a blob created by Encrypt. The error means the key
// is wrong or the data is corrupted: authenticity is checked by GCM.
func Decrypt(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < nonceSize {
		return nil, errors.New("the blob is shorter than the nonce")
	}
	plaintext, err := gcm.Open(nil, blob[:nonceSize], blob[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("decryption: %w", err)
	}
	return plaintext, nil
}

// ValueHash returns the keyed hash of a value: HMAC-SHA256 over typ + "\x00" + value.
// The type is part of the hash, so one value in different roles gets different sums;
// the separator keeps the pair ("HOST","ab") and ("HOST\x00a","b").
func ValueHash(key []byte, typ string, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(typ))
	mac.Write([]byte{0})
	mac.Write(value)
	return mac.Sum(nil)
}

// newGCM builds an AEAD from the key.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES key: %w", err)
	}
	return cipher.NewGCM(block)
}
