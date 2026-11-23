package core

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"
)

func TestBridgeCredentialMethodsAreIndependent(t *testing.T) {
	credential := rand.Text()
	other := rand.Text()
	for other == credential {
		other = rand.Text()
	}
	browser := "cc-connect, cc-connect-token." + base64.RawURLEncoding.EncodeToString([]byte(credential))
	bs := &BridgeServer{token: credential}

	for _, tc := range []struct {
		name, authorization, header, protocol string
		want                                  bool
	}{
		{name: "bearer", authorization: "Bearer " + credential, want: true},
		{name: "header", authorization: "Bearer " + other, header: credential, want: true},
		{name: "browser", authorization: "Bearer " + other, header: other, protocol: browser, want: true},
		{name: "none validated", authorization: "Bearer " + other, header: other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{URL: &url.URL{Path: "/bridge/ws"}, Header: make(http.Header)}
			r.Header.Set("Authorization", tc.authorization)
			r.Header.Set("X-Bridge-Token", tc.header)
			r.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
			if got := bs.authenticate(r); got != tc.want {
				t.Fatalf("authenticate() = %t, want %t", got, tc.want)
			}
		})
	}
}
