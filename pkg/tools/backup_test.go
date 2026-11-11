package tools

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"testing"
)

func TestEncryptAESGCM(t *testing.T) {
	key := []byte("secret_test_encryption_key_1234")
	data := []byte("important database state and memories")

	ciphertext, err := EncryptAESGCM(data, key)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}

	// Decrypt and verify
	h := sha256.Sum256(key)
	block, _ := aes.NewCipher(h[:])
	gcm, _ := cipher.NewGCM(block)

	nonceSize := gcm.NonceSize()
	nonce, cipherOnly := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, cipherOnly, nil)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	if !bytes.Equal(plaintext, data) {
		t.Fatalf("decrypted = %q, want %q", plaintext, data)
	}
}
