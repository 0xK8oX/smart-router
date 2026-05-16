package crypto

import (
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := "sk-test-api-key-12345"

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if ciphertext == plaintext {
		t.Fatal("ciphertext should not equal plaintext")
	}

	decrypted, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("decrypted text does not match original: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i)
	}

	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(i + 1)
	}

	plaintext := "sk-test-api-key-12345"

	ciphertext, err := Encrypt(plaintext, key1)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = Decrypt(ciphertext, key2)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key, got nil")
	}
}
