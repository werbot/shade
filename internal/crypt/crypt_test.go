package crypt_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/crypt"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	blob, err := crypt.Encrypt(key, []byte("db.prod.local"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("db.prod.local")) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	got, err := crypt.Decrypt(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "db.prod.local" {
		t.Fatalf("got %q", got)
	}
}

func TestEncryptIsNonDeterministic(t *testing.T) {
	key := make([]byte, 32)
	a, _ := crypt.Encrypt(key, []byte("x"))
	b, _ := crypt.Encrypt(key, []byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("same nonce reused")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	blob, _ := crypt.Encrypt(make([]byte, 32), []byte("x"))
	if _, err := crypt.Decrypt(bytes.Repeat([]byte{1}, 32), blob); err == nil {
		t.Fatal("wrong key must fail authentication")
	}
}

func TestValueHashIsTypeScoped(t *testing.T) {
	key := make([]byte, 32)
	if bytes.Equal(crypt.ValueHash(key, "HOST", []byte("x")), crypt.ValueHash(key, "USER", []byte("x"))) {
		t.Fatal("hash must depend on type")
	}
}

func TestLoadOrCreateKeyIsStableAndPrivate(t *testing.T) {
	home := t.TempDir()
	k1, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := crypt.LoadOrCreateKey(home)
	if !bytes.Equal(k1, k2) {
		t.Fatal("key must be stable across calls")
	}
	if len(k1) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(k1))
	}
	info, err := os.Stat(filepath.Join(home, "key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %o", info.Mode().Perm())
	}
}
