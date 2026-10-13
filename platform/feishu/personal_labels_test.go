package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func labelFixture(t *testing.T, handler http.HandlerFunc) *Platform {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Platform{labels: &personalLabelClient{app: "cli_test", secret: "private-secret", base: server.URL, accounts: server.URL, dir: filepath.Join(t.TempDir(), "auth"), http: server.Client()}}
}
func TestPersonalLabels_OAuthBindsRequesterAndPersists(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			p := labelFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/v1/device_authorization":
					id, secret, ok := r.BasicAuth()
					r.ParseForm()
					if !ok || id != "cli_test" || secret != "private-secret" || r.Form.Get("scope") != labelScopes {
						t.Error("invalid device request")
					}
					fmt.Fprint(w, `{"device_code":"private-device","verification_uri_complete":"https://accounts.feishu.cn/verify?x=1","expires_in":20,"interval":1}`)
				case labelTokenPath:
					r.ParseForm()
					if r.Form.Get("device_code") != "private-device" {
						t.Error("missing device code")
					}
					fmt.Fprint(w, `{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600,"refresh_token_expires_in":86400}`)
				case "/open-apis/authen/v1/user_info":
					if r.Header.Get("Authorization") != "Bearer private-access" {
						t.Error("missing user token")
					}
					user := "ou_owner"
					if wrong {
						user = "ou_else"
					}
					fmt.Fprintf(w, `{"code":0,"data":{"open_id":%q}}`, user)
				default:
					t.Errorf("unexpected %s", r.URL.Path)
				}
			})
			notified := false
			err := p.AuthorizePersonalLabels(context.Background(), "ou_owner", func(link string) error {
				notified = true
				if link != "https://accounts.feishu.cn/verify?x=1" {
					t.Error("changed link")
				}
				return nil
			})
			if !notified {
				t.Fatal("no authorization link")
			}
			if wrong {
				if err == nil || !strings.Contains(err.Error(), "wrong_user") || p.PersonalLabelsAuthorized("ou_owner") {
					t.Fatal("saved different user's credentials")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !p.PersonalLabelsAuthorized("ou_owner") || p.PersonalLabelsAuthorized("ou_other") {
				t.Fatal("wrong identity scope")
			}
			info, _ := os.Stat(p.labels.path("ou_owner"))
			if info.Mode().Perm() != 0600 {
				t.Fatal("token is not private")
			}
			info, _ = os.Stat(p.labels.dir)
			if info.Mode().Perm() != 0700 {
				t.Fatal("directory is not private")
			}
			copy := *p.labels
			copy.app = "cli_other"
			if _, e := copy.read("ou_owner"); e == nil {
				t.Fatal("credentials shared across apps")
			}
		})
	}
}
func TestPersonalLabels_RefreshPreservesCredentialsOnFailure(t *testing.T) {
	failure := true
	var mu sync.Mutex
	p := labelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != labelTokenPath {
			t.Error("unexpected API")
		}
		r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-old" {
			t.Error("wrong refresh")
		}
		if failure {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"server_error","error_description":"private-refresh"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600,"refresh_token_expires_in":86400}`)
	})
	old := labelToken{User: "owner", Access: "access-old", Refresh: "refresh-old", Expires: time.Now().Add(-time.Minute), RefreshExpires: time.Now().Add(time.Hour)}
	if err := writeLabelJSON(p.labels.path("owner"), old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.labels.path("owner"))
	_, err := p.labels.token(context.Background(), "owner")
	if err == nil || strings.Contains(err.Error(), "private-refresh") {
		t.Fatal("error missing or leaked secret")
	}
	after, _ := os.ReadFile(p.labels.path("owner"))
	if string(before) != string(after) {
		t.Fatal("failed refresh deleted credentials")
	}
	mu.Lock()
	failure = false
	mu.Unlock()
	tok, err := p.labels.token(context.Background(), "owner")
	if err != nil || tok != "access-new" {
		t.Fatal("refresh failed", err)
	}
	saved, _ := p.labels.read("owner")
	if saved.Refresh != "refresh-new" {
		t.Fatal("rotation not saved")
	}
}
func TestPersonalLabels_ListAndPartialAddFailure(t *testing.T) {
	p := labelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("used app token")
		}
		switch r.URL.Path {
		case "/open-apis/im/v1/groups":
			if r.Method == "POST" {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if b["feed_group_creator"] == nil {
					t.Error("bad create payload")
				}
				fmt.Fprint(w, `{"code":0,"data":{"group_id":"ofg_new"}}`)
				return
			}
			if r.URL.Query().Get("page_token") == "" {
				fmt.Fprint(w, `{"code":0,"data":{"groups":[{"group_id":"ofg_one","name":"One","type":"normal"},{"group_id":"ofg_rule","name":"Rule","type":"rule"}],"deleted_groups":[{"group_id":"ofg_deleted"}],"has_more":true,"page_token":"next"}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"groups":[{"group_id":"ofg_two","name":"Two","type":"normal"}],"has_more":false}}`)
			}
		case "/open-apis/im/v1/groups/ofg_new/batch_add_item":
			fmt.Fprint(w, `{"code":0,"data":{"failed_items":[{"error_code":240001}]}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	p.labels.saveToken("owner", labelTokenResponse{Access: "access", Expires: 3600}, labelToken{})
	list, err := p.ListPersonalLabels(context.Background(), "owner")
	if err != nil || len(list) != 2 || list[1].ID != "ofg_two" {
		t.Fatalf("bad pagination: %+v %v", list, err)
	}
	g, err := p.CreatePersonalLabel(context.Background(), "owner", "New")
	if err != nil || g.ID != "ofg_new" {
		t.Fatal(err)
	}
	if err = p.AddPersonalLabel(context.Background(), "owner", "oc_chat", g.ID); err == nil {
		t.Fatal("partial add failure ignored")
	}
}

var _ core.PersonalLabelManager = (*Platform)(nil)

func TestPersonalLabels_CredentialRefreshIsExclusiveAcrossInstances(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	p := labelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		fmt.Fprint(w, `{"access_token":"rotated","refresh_token":"new-refresh","expires_in":3600,"refresh_token_expires_in":3600}`)
	})
	old := labelToken{User: "owner", Refresh: "once-only", RefreshExpires: time.Now().Add(time.Hour)}
	writeLabelJSON(p.labels.path("owner"), old)
	done := make(chan error, 1)
	go func() { _, e := p.labels.token(context.Background(), "owner"); done <- e }()
	<-started
	copy := *p.labels
	if _, err := copy.token(context.Background(), "owner"); err == nil {
		t.Error("two instances rotated the same token")
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
