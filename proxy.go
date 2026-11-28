//go:build ignore

// proxy routes /ruliu/codex/callback → :8082/ruliu/callback
//              /ruliu/cc/callback    → :8083/ruliu/callback
// Run: go run proxy.go

package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func rewriteProxy(target *url.URL, stripPrefix string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(r *http.Request) {
		orig(r)
		// /ruliu/codex/callback → /ruliu/callback
		r.URL.Path = "/ruliu" + strings.TrimPrefix(r.URL.Path, stripPrefix)
		r.URL.RawPath = ""
		log.Printf("proxy → %s%s", target.Host, r.URL.Path)
	}
	return proxy
}

func main() {
	codex, _ := url.Parse("http://localhost:8082")
	cc, _ := url.Parse("http://localhost:8083")

	mux := http.NewServeMux()
	mux.Handle("/ruliu/codex/", rewriteProxy(codex, "/ruliu/codex"))
	mux.Handle("/ruliu/cc/", rewriteProxy(cc, "/ruliu/cc"))

	log.Println("proxy listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
