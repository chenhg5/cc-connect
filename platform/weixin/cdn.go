package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxWeixinMediaBytes = 100 << 20

var hex32RE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// aesECBPaddedSize returns ciphertext length for AES-128-ECB with PKCS#7 padding.
func aesECBPaddedSize(plaintextLen int) int {
	if plaintextLen < 0 {
		return 0
	}
	return ((plaintextLen + aes.BlockSize) / aes.BlockSize) * aes.BlockSize
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	if blockSize <= 0 || blockSize > 255 {
		panic("invalid block size")
	}
	n := blockSize - (len(b) % blockSize)
	pad := bytes.Repeat([]byte{byte(n)}, n)
	return append(b, pad...)
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded length %d", len(b))
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	for i := len(b) - n; i < len(b); i++ {
		if b[i] != byte(n) {
			return nil, fmt.Errorf("invalid pkcs7 padding")
		}
	}
	return b[:len(b)-n], nil
}

func encryptAESECB(plaintext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("aes key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out, nil
}

func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("aes key must be 16 bytes, got %d", len(key))
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d not aligned to block", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(out, aes.BlockSize)
}

// parseAesKey decodes CDNMedia.aes_key: base64(raw 16 bytes) or base64(32-char hex ASCII) → 16 bytes.
func parseAesKey(aesKeyBase64, label string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(aesKeyBase64))
	if err != nil {
		return nil, fmt.Errorf("%s: aes_key base64: %w", label, err)
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		s := string(decoded)
		if hex32RE.MatchString(s) {
			k, err := hex.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("%s: aes_key hex inside base64: %w", label, err)
			}
			return k, nil
		}
	}
	return nil, fmt.Errorf("%s: aes_key must be 16 raw bytes or 32-char hex (base64-wrapped), got %d bytes after base64", label, len(decoded))
}

func buildCdnDownloadURL(encryptedQueryParam, cdnBase string) string {
	return fmt.Sprintf("%s/download?encrypted_query_param=%s",
		strings.TrimRight(cdnBase, "/"),
		url.QueryEscape(encryptedQueryParam))
}

func buildCdnUploadURL(cdnBase, uploadParam, filekey string) string {
	return fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
		strings.TrimRight(cdnBase, "/"),
		url.QueryEscape(uploadParam),
		url.QueryEscape(filekey))
}

func fetchCdnBytes(ctx context.Context, client *http.Client, fullURL, label string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: new request: %w", label, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: get: %w", label, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWeixinMediaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", label, err)
	}
	if len(body) > maxWeixinMediaBytes {
		return nil, fmt.Errorf("%s: CDN body exceeds %d bytes", label, maxWeixinMediaBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: CDN http %d: %s", label, resp.StatusCode, truncateForLog(body, 256))
	}
	return body, nil
}

func downloadAndDecryptCDN(ctx context.Context, client *http.Client, cdnBase, encParam, aesKeyBase64, label string) ([]byte, error) {
	key, err := parseAesKey(aesKeyBase64, label)
	if err != nil {
		return nil, err
	}
	u := buildCdnDownloadURL(encParam, cdnBase)
	enc, err := fetchCdnBytes(ctx, client, u, label)
	if err != nil {
		return nil, err
	}
	plain, err := decryptAESECB(enc, key)
	if err != nil {
		return nil, fmt.Errorf("%s: decrypt: %w", label, err)
	}
	return plain, nil
}

func downloadPlainCDN(ctx context.Context, client *http.Client, cdnBase, encParam, label string) ([]byte, error) {
	u := buildCdnDownloadURL(encParam, cdnBase)
	return fetchCdnBytes(ctx, client, u, label)
}

const cdnUploadMaxRetries = 3

func uploadBufferToCDN(ctx context.Context, client *http.Client, cdnBase, uploadParam, filekey string, plaintext, aesKey []byte, label string) (downloadParam string, err error) {
	ciphertext, err := encryptAESECB(plaintext, aesKey)
	if err != nil {
		return "", fmt.Errorf("%s: encrypt: %w", label, err)
	}
	u := buildCdnUploadURL(cdnBase, uploadParam, filekey)
	var lastErr error
	for attempt := 1; attempt <= cdnUploadMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(ciphertext))
		if err != nil {
			return "", fmt.Errorf("%s: new request: %w", label, err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			slog.Warn("weixin: CDN upload request failed", "label", label, "attempt", attempt, "error", err)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			msg := resp.Header.Get("x-error-message")
			if msg == "" {
				msg = resp.Status
			}
			return "", fmt.Errorf("%s: CDN upload client error %d: %s", label, resp.StatusCode, msg)
		}
		if resp.StatusCode != http.StatusOK {
			msg := resp.Header.Get("x-error-message")
			if msg == "" {
				msg = fmt.Sprintf("status %d", resp.StatusCode)
			}
			lastErr = fmt.Errorf("%s: CDN upload server error: %s", label, msg)
			slog.Warn("weixin: CDN upload server error", "label", label, "attempt", attempt, "error", lastErr)
			continue
		}
		dl := resp.Header.Get("x-encrypted-param")
		if dl == "" {
			lastErr = fmt.Errorf("%s: CDN response missing x-encrypted-param", label)
			slog.Warn("weixin: CDN upload bad response", "label", label, "attempt", attempt)
			continue
		}
		return dl, nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("%s: CDN upload failed after %d attempts: %w", label, cdnUploadMaxRetries, lastErr)
	}
	return "", fmt.Errorf("%s: CDN upload failed after %d attempts", label, cdnUploadMaxRetries)
}

func md5Hex(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

func detectImageMime(b []byte) string {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return "image/jpeg"
	}
	if len(b) >= 8 && string(b[0:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(b) >= 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "image/jpeg"
}
