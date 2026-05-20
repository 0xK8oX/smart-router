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

func TestEncryptInvalidKeyLength(t *testing.T) {
	_, err := Encrypt("plaintext", []byte("short"))
	if err == nil {
		t.Fatal("expected error for invalid key length, got nil")
	}

	_, err = Encrypt("plaintext", make([]byte, 16))
	if err == nil {
		t.Fatal("expected error for 16-byte key, got nil")
	}

	_, err = Encrypt("plaintext", make([]byte, 24))
	if err == nil {
		t.Fatal("expected error for 24-byte key, got nil")
	}
}

func TestDecryptCiphertextTooShort(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	_, err := Decrypt("YWJj", key) // base64("abc") = 3 bytes, less than nonce size
	if err == nil {
		t.Fatal("expected error for ciphertext too short, got nil")
	}
}

func TestDecryptCorruptedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := "sk-test-api-key-12345"
	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Corrupt the ciphertext by flipping a character in the base64 string
	corrupted := ciphertext[:len(ciphertext)-1] + "X"
	if corrupted == ciphertext {
		corrupted = ciphertext + "X"
	}

	_, err = Decrypt(corrupted, key)
	if err == nil {
		t.Fatal("expected error for corrupted ciphertext, got nil")
	}
}
