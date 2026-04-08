package weixin

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestAesECBPaddedSize(t *testing.T) {
	if aesECBPaddedSize(0) != 16 {
		t.Fatalf("0 -> %d", aesECBPaddedSize(0))
	}
	if aesECBPaddedSize(1) != 16 {
		t.Fatalf("1 -> %d", aesECBPaddedSize(1))
	}
	if aesECBPaddedSize(16) != 32 {
		t.Fatalf("16 -> %d (pkcs7 adds a full block when aligned)", aesECBPaddedSize(16))
	}
	if aesECBPaddedSize(17) != 32 {
		t.Fatalf("17 -> %d", aesECBPaddedSize(17))
	}
}

func TestEncryptDecryptAESECB_RoundTrip(t *testing.T) {
	key := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	plain := []byte("hello weixin cdn")
	ct, err := encryptAESECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptAESECB(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q want %q", got, plain)
	}
}

func TestParseAesKey_Raw16(t *testing.T) {
	key := []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6}
	b64 := base64.StdEncoding.EncodeToString(key)
	got, err := parseAesKey(b64, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("mismatch")
	}
}

func TestParseAesKey_HexWrapped(t *testing.T) {
	raw, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	// Simulate API: base64(ASCII hex string)
	wrapped := base64.StdEncoding.EncodeToString([]byte("00112233445566778899aabbccddeeff"))
	got, err := parseAesKey(wrapped, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("got %x want %x", got, raw)
	}
}

func TestBuildCdnDownloadURL(t *testing.T) {
	u := buildCdnDownloadURL("abc+def", "https://example/c2c")
	if u != "https://example/c2c/download?encrypted_query_param=abc%2Bdef" {
		t.Fatalf("got %q", u)
	}
}

func TestDetectImageMime(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if detectImageMime(png) != "image/png" {
		t.Fatalf("png: %s", detectImageMime(png))
	}
	if detectImageMime([]byte{0xff, 0xd8, 0xff}) != "image/jpeg" {
		t.Fatal("jpeg magic")
	}
}
