package ruliu

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	testAESKeyMaterial = "0123456789abcdef" // 16 bytes -> 22-char EncodingAESKey
	testCallbackToken  = "callback-token"
)

// encryptECBWithPKCS7 is the inverse of decryptECB + pkcs7Unpad. It builds
// fixtures on the fly so the tests do not depend on opaque ciphertext blobs.
func encryptECBWithPKCS7(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, 0, len(plaintext)+padding)
	padded = append(padded, plaintext...)
	padded = append(padded, bytes.Repeat([]byte{byte(padding)}, padding)...)

	out := make([]byte, len(padded))
	for start := 0; start < len(padded); start += aes.BlockSize {
		block.Encrypt(out[start:start+aes.BlockSize], padded[start:start+aes.BlockSize])
	}
	return out
}

func encodingAESKeyFor(t *testing.T, key []byte) string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(key)
	if len(encoded) != 24 || !strings.HasSuffix(encoded, "==") {
		t.Fatalf("unexpected base64 key form %q", encoded)
	}
	// Ruliu (like WeChat Work) ships the key as the 22-char prefix, padding dropped.
	return strings.TrimSuffix(encoded, "==")
}

func TestDecodeEncodingAESKey(t *testing.T) {
	want := []byte(testAESKeyMaterial)
	valid := encodingAESKeyFor(t, want)

	t.Run("valid", func(t *testing.T) {
		key, err := decodeEncodingAESKey(valid)
		if err != nil {
			t.Fatalf("decodeEncodingAESKey(%q): %v", valid, err)
		}
		if !bytes.Equal(key, want) {
			t.Fatalf("key = %x, want %x", key, want)
		}
	})

	t.Run("rejects wrong length", func(t *testing.T) {
		for _, bad := range []string{"", valid + "A", valid[:21]} {
			if _, err := decodeEncodingAESKey(bad); err == nil {
				t.Fatalf("decodeEncodingAESKey(%q) = nil error, want error", bad)
			}
		}
	})

	t.Run("rejects invalid base64", func(t *testing.T) {
		if _, err := decodeEncodingAESKey(strings.Repeat("!", 22)); err == nil {
			t.Fatal("expected error for non-base64 key")
		}
	})
}

func TestDecryptECBRoundTrip(t *testing.T) {
	key := []byte(testAESKeyMaterial)
	cases := []struct {
		name      string
		plaintext string
	}{
		{name: "shorter than one block", plaintext: "hi"},
		{name: "exactly one block", plaintext: strings.Repeat("a", aes.BlockSize)},
		{name: "multiple blocks with partial tail", plaintext: "ruliu webhook callback payload 如流"},
		{name: "empty plaintext", plaintext: ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ciphertext := encryptECBWithPKCS7(t, []byte(c.plaintext), key)
			padded, err := decryptECB(ciphertext, key)
			if err != nil {
				t.Fatalf("decryptECB: %v", err)
			}
			plaintext, err := pkcs7Unpad(padded, aes.BlockSize)
			if err != nil {
				t.Fatalf("pkcs7Unpad: %v", err)
			}
			if string(plaintext) != c.plaintext {
				t.Fatalf("plaintext = %q, want %q", plaintext, c.plaintext)
			}
		})
	}
}

func TestDecryptECBErrors(t *testing.T) {
	key := []byte(testAESKeyMaterial)

	t.Run("rejects empty ciphertext", func(t *testing.T) {
		if _, err := decryptECB(nil, key); err == nil {
			t.Fatal("expected error for empty ciphertext")
		}
	})

	t.Run("rejects partial block ciphertext", func(t *testing.T) {
		if _, err := decryptECB(make([]byte, aes.BlockSize-1), key); err == nil {
			t.Fatal("expected error for ciphertext that is not a block multiple")
		}
	})

	t.Run("rejects invalid key size", func(t *testing.T) {
		ciphertext := encryptECBWithPKCS7(t, []byte("data"), key)
		if _, err := decryptECB(ciphertext, []byte("too-short-key")); err == nil {
			t.Fatal("expected error for 13-byte key")
		}
	})
}

func TestPKCS7Unpad(t *testing.T) {
	block := aes.BlockSize
	fullBlockOf := func(fill byte) []byte {
		return bytes.Repeat([]byte{fill}, block)
	}

	cases := []struct {
		name    string
		in      []byte
		want    []byte
		wantErr bool
	}{
		{
			name: "single byte padding",
			in:   append(bytes.Repeat([]byte("A"), block-1), 1),
			want: bytes.Repeat([]byte("A"), block-1),
		},
		{
			name: "full block padding",
			in:   fullBlockOf(byte(block)),
			want: []byte{},
		},
		{
			name: "padding of four",
			in:   append(bytes.Repeat([]byte("B"), block-4), 4, 4, 4, 4),
			want: bytes.Repeat([]byte("B"), block-4),
		},
		{name: "empty input", in: nil, wantErr: true},
		{name: "not a block multiple", in: []byte("short"), wantErr: true},
		{name: "zero padding", in: append(fullBlockOf('A'), 0), wantErr: true},
		{name: "padding larger than block", in: append(fullBlockOf('A'), byte(block+1)), wantErr: true},
		{
			name:    "inconsistent padding bytes",
			in:      []byte{'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 'A', 2, 3},
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pkcs7Unpad(c.in, block)
			if c.wantErr {
				if err == nil {
					t.Fatalf("pkcs7Unpad(%x) = %x, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pkcs7Unpad(%x): %v", c.in, err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("pkcs7Unpad(%x) = %x, want %x", c.in, got, c.want)
			}
		})
	}
}

func TestDecodeURLBase64(t *testing.T) {
	// Bytes chosen so standard base64 contains both '+' and '/', which Ruliu
	// swaps for '-' and '_' when it URL-encodes the ciphertext.
	payload := []byte{0xfb, 0xff, 0xfe, 0x01, 0x02, 0x03}
	standard := base64.StdEncoding.EncodeToString(payload)
	if !strings.ContainsAny(standard, "+/") {
		t.Fatalf("fixture does not exercise URL-safe substitution: %q", standard)
	}

	urlSafe := strings.NewReplacer("+", "-", "/", "_").Replace(standard)

	cases := []struct {
		name    string
		in      string
		want    []byte
		wantErr bool
	}{
		{name: "standard base64", in: standard, want: payload},
		{name: "url-safe base64 with padding", in: urlSafe, want: payload},
		{name: "url-safe base64 without padding", in: strings.TrimRight(urlSafe, "="), want: payload},
		{name: "invalid length", in: "A", wantErr: true},
		{name: "invalid characters", in: "!!!!", wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeURLBase64(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("decodeURLBase64(%q) = %x, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeURLBase64(%q): %v", c.in, err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("decodeURLBase64(%q) = %x, want %x", c.in, got, c.want)
			}
		})
	}
}

func TestVerifySignature(t *testing.T) {
	p := &Platform{token: testCallbackToken}
	const (
		rn        = "nonce-value"
		timestamp = "1700000000"
	)
	sum := md5.Sum([]byte(rn + timestamp + testCallbackToken))
	signature := hex.EncodeToString(sum[:])

	if !p.verifySignature(signature, rn, timestamp) {
		t.Fatal("expected signature to verify")
	}
	if !p.verifySignature(strings.ToUpper(signature), rn, timestamp) {
		t.Fatal("expected signature comparison to be case-insensitive")
	}

	rejected := []struct {
		name      string
		signature string
		rn        string
		timestamp string
		platform  *Platform
	}{
		{name: "empty signature", signature: "", rn: rn, timestamp: timestamp, platform: p},
		{name: "wrong nonce", signature: signature, rn: "other-nonce", timestamp: timestamp, platform: p},
		{name: "wrong timestamp", signature: signature, rn: rn, timestamp: "1700000001", platform: p},
		{name: "wrong token", signature: signature, rn: rn, timestamp: timestamp, platform: &Platform{token: "other-token"}},
	}
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			if c.platform.verifySignature(c.signature, c.rn, c.timestamp) {
				t.Fatal("expected signature to be rejected")
			}
		})
	}
}

func TestDecryptPayload(t *testing.T) {
	key := []byte(testAESKeyMaterial)
	p := &Platform{token: testCallbackToken, aesKey: key}
	plaintext := []byte(`{"eventtype":"bot_message","message":{"header":{"fromuserid":"u1"}}}`)
	ciphertext := encryptECBWithPKCS7(t, plaintext, key)
	urlSafe := base64.URLEncoding.EncodeToString(ciphertext)

	t.Run("url-safe with padding", func(t *testing.T) {
		got, err := p.decryptPayload(urlSafe)
		if err != nil {
			t.Fatalf("decryptPayload: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("payload = %q, want %q", got, plaintext)
		}
	})

	t.Run("url-safe without padding", func(t *testing.T) {
		got, err := p.decryptPayload(strings.TrimRight(urlSafe, "="))
		if err != nil {
			t.Fatalf("decryptPayload: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("payload = %q, want %q", got, plaintext)
		}
	})

	t.Run("rejects garbage base64", func(t *testing.T) {
		if _, err := p.decryptPayload("!!!"); err == nil {
			t.Fatal("expected error for invalid base64 ciphertext")
		}
	})

	t.Run("rejects ciphertext from another key", func(t *testing.T) {
		otherCiphertext := encryptECBWithPKCS7(t, plaintext, []byte("fedcba9876543210"))
		if _, err := p.decryptPayload(base64.URLEncoding.EncodeToString(otherCiphertext)); err == nil {
			t.Fatal("expected padding error when decrypting with the wrong key")
		}
	})
}

func TestNewValidatesCryptoOptions(t *testing.T) {
	validKey := encodingAESKeyFor(t, []byte(testAESKeyMaterial))

	missing := []struct {
		name string
		opts map[string]any
	}{
		{name: "no options", opts: map[string]any{}},
		{name: "missing webhook", opts: map[string]any{"token": "t", "encoding_aes_key": validKey}},
		{name: "missing token", opts: map[string]any{"webhook": "https://example.com/hook", "encoding_aes_key": validKey}},
		{name: "missing aes key", opts: map[string]any{"webhook": "https://example.com/hook", "token": "t"}},
		{name: "invalid aes key", opts: map[string]any{"webhook": "https://example.com/hook", "token": "t", "encoding_aes_key": "not-a-valid-key"}},
	}
	for _, c := range missing {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.opts); err == nil {
				t.Fatal("expected New to fail")
			}
		})
	}

	t.Run("decodes aes key and applies defaults", func(t *testing.T) {
		platform, err := New(map[string]any{
			"webhook":          "https://example.com/hook",
			"token":            "t",
			"encoding_aes_key": validKey,
			"allow_from":       "*",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		p, ok := platform.(*Platform)
		if !ok {
			t.Fatalf("New returned %T, want *Platform", platform)
		}
		if !bytes.Equal(p.aesKey, []byte(testAESKeyMaterial)) {
			t.Fatalf("aesKey = %x, want %x", p.aesKey, testAESKeyMaterial)
		}
		if p.port != defaultPort {
			t.Fatalf("port = %q, want %q", p.port, defaultPort)
		}
		if p.callbackPath != defaultCallbackURL {
			t.Fatalf("callbackPath = %q, want %q", p.callbackPath, defaultCallbackURL)
		}
	})
}
