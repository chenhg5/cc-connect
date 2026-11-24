package feishu

import (
	"fmt"
	"testing"
	"time"
)

func TestImageCacheHitTTLAndEviction(t *testing.T) {
	p := &Platform{}
	p.storeCachedImage("m1\x00k1", []byte("abc"), "image/png")

	data, mime, ok := p.getCachedImage("m1\x00k1")
	if !ok || string(data) != "abc" || mime != "image/png" {
		t.Fatalf("cache hit = %q,%q,%v; want abc,image/png,true", data, mime, ok)
	}

	// Expired entries are treated as misses and removed.
	p.imageCacheMu.Lock()
	entry := p.imageCache["m1\x00k1"]
	entry.at = time.Now().Add(-imageCacheTTL - time.Second)
	p.imageCache["m1\x00k1"] = entry
	p.imageCacheMu.Unlock()
	if _, _, ok := p.getCachedImage("m1\x00k1"); ok {
		t.Fatal("expired image cache entry was returned")
	}

	// The cache is bounded by entry count.
	for i := 0; i < imageCacheMaxEntries+3; i++ {
		p.storeCachedImage(fmt.Sprintf("k%d", i), []byte{byte(i)}, "image/png")
	}
	p.imageCacheMu.Lock()
	entries, bytes := len(p.imageCache), p.imageCacheBytes
	p.imageCacheMu.Unlock()
	if entries > imageCacheMaxEntries {
		t.Fatalf("cache entries = %d, want <= %d", entries, imageCacheMaxEntries)
	}
	if bytes > imageCacheMaxBytes {
		t.Fatalf("cache bytes = %d, want <= %d", bytes, imageCacheMaxBytes)
	}
}
