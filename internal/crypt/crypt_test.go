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

// A key of the wrong length would not decrypt the old values, so the loader
// must fail rather than regenerate the file: otherwise a silent "repair" would wipe
// the whole mapping. The test pins down both the refusal and the byte-for-byte preservation of the file.
func TestLoadOrCreateKeyRejectsWrongLength(t *testing.T) {
	for _, size := range []int{16, 64} {
		home := t.TempDir()
		path := filepath.Join(home, "key")
		stored := bytes.Repeat([]byte{7}, size)
		if err := os.WriteFile(path, stored, 0o600); err != nil {
			t.Fatal(err)
		}
		for attempt := range 2 {
			key, err := crypt.LoadOrCreateKey(home)
			if err == nil {
				t.Fatalf("%d bytes: a key of the wrong length was accepted as %d bytes", size, len(key))
			}
			if key != nil {
				t.Fatalf("%d bytes: a key came back together with the error", size)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, stored) {
				t.Fatalf("%d bytes: attempt %d overwrote the key file", size, attempt+1)
			}
		}
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
