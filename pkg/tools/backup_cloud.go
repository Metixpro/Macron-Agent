package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const ToolEncryptedBackup = "encrypted_cloud_backup"

// EncryptAESGCM encrypts data using AES-256-GCM.
func EncryptAESGCM(data []byte, key []byte) ([]byte, error) {
	if len(key) != 32 {
		h := sha256.Sum256(key)
		key = h[:]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

// CreateEncryptedBackup packages essential agent state and encrypts it with MACRON_SECRET_KEY.
func (r *Registry) CreateEncryptedBackup(ctx context.Context, sessionID, remoteProvider, destDir string) (string, error) {
	key, err := config.SecretKey()
	if err != nil || key == "" {
		return "", fmt.Errorf("MACRON_SECRET_KEY is required for encrypted backup: %w", err)
	}

	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)

	// Files to include
	home := config.Home()
	targets := []string{
		config.FileDB,
		config.FileSoul,
		config.FileUser,
		config.FileMemory,
	}

	included := 0
	for _, fName := range targets {
		fullPath := filepath.Join(home, fName)
		fi, err := os.Stat(fullPath)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		hdr := &tar.Header{
			Name:    fName,
			Size:    int64(len(data)),
			Mode:    0o600,
			ModTime: fi.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err == nil {
			if _, err := tw.Write(data); err == nil {
				included++
			}
		}
	}

	_ = tw.Close()
	_ = gw.Close()

	if included == 0 {
		return "", fmt.Errorf("no database or memory files found to backup in %s", home)
	}

	unencryptedData := buf.Bytes()
	unencryptedHash := sha256.Sum256(unencryptedData)

	encryptedBytes, err := EncryptAESGCM(unencryptedData, []byte(key))
	if err != nil {
		return "", fmt.Errorf("encryption failed: %w", err)
	}

	backupName := fmt.Sprintf("macron_backup_%s.tar.gz.enc", time.Now().Format("20060102_150405"))
	outPath := backupName
	if destDir != "" {
		outPath = filepath.Join(destDir, backupName)
	}

	// Write encrypted archive
	if err := r.Machine.WriteFile(sessionID, outPath, encryptedBytes); err != nil {
		return "", fmt.Errorf("save backup: %w", err)
	}

	return fmt.Sprintf("🔒 **Encrypted Cloud Backup Created**\n- File: `%s` (%d bytes encrypted with AES-256-GCM)\n- Included files: %d\n- SHA-256 Checksum: `%s`",
		outPath, len(encryptedBytes), included, hex.EncodeToString(unencryptedHash[:16])), nil
}
