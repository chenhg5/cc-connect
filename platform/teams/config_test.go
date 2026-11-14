package teams

import "testing"

func validOpts() map[string]any {
	return map[string]any{
		"app_id":       "app-123",
		"app_password": "secret",
		"tenant_id":    "tenant-abc",
	}
}

func TestParseConfig_Valid(t *testing.T) {
	c, err := parseConfig(validOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.appID != "app-123" || c.appPassword != "secret" {
		t.Fatalf("app credentials not parsed: %+v", c)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	c, err := parseConfig(validOpts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.webhookPort != defaultWebhookPort {
		t.Errorf("webhookPort = %q, want %q", c.webhookPort, defaultWebhookPort)
	}
	if c.webhookPath != defaultWebhookPath {
		t.Errorf("webhookPath = %q, want %q", c.webhookPath, defaultWebhookPath)
	}
	if c.sessionScope != "thread" {
		t.Errorf("sessionScope = %q, want thread", c.sessionScope)
	}
}

func TestParseConfig_MissingAppID(t *testing.T) {
	opts := validOpts()
	delete(opts, "app_id")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing app_id")
	}
}

func TestParseConfig_MissingAppPassword(t *testing.T) {
	opts := validOpts()
	delete(opts, "app_password")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing app_password")
	}
}

func TestParseConfig_MissingTenantID(t *testing.T) {
	opts := validOpts()
	delete(opts, "tenant_id")
	if _, err := parseConfig(opts); err == nil {
		t.Fatal("expected error for missing tenant_id (connector is single-tenant only)")
	}
}

func TestParseConfig_WebhookPathNormalized(t *testing.T) {
	opts := validOpts()
	opts["webhook_path"] = "teams/hook"
	c, err := parseConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.webhookPath != "/teams/hook" {
		t.Errorf("webhookPath = %q, want /teams/hook", c.webhookPath)
	}
}

func TestParseConfig_SessionScope(t *testing.T) {
	cases := map[string]string{
		"":        "thread",
		"thread":  "thread",
		"channel": "channel",
		"user":    "user",
		"bogus":   "thread",
	}
	for in, want := range cases {
		opts := validOpts()
		opts["session_scope"] = in
		c, err := parseConfig(opts)
		if err != nil {
			t.Fatalf("scope %q: unexpected error: %v", in, err)
		}
		if c.sessionScope != want {
			t.Errorf("session_scope %q -> %q, want %q", in, c.sessionScope, want)
		}
	}
}

func TestParseConfig_CardUpdateIntervalMS(t *testing.T) {
	cases := map[any]int{
		int64(900): 900,
		900:        900,
		0:          defaultCardUpdateIntervalMS,
		-1:         defaultCardUpdateIntervalMS,
		"nope":     defaultCardUpdateIntervalMS,
	}
	for in, want := range cases {
		opts := validOpts()
		opts["card_update_interval_ms"] = in
		c, err := parseConfig(opts)
		if err != nil {
			t.Fatalf("interval %v: unexpected error: %v", in, err)
		}
		if c.cardUpdateIntervalMS != want {
			t.Errorf("card_update_interval_ms %v -> %d, want %d", in, c.cardUpdateIntervalMS, want)
		}
	}
	c, _ := parseConfig(validOpts())
	if c.cardUpdateIntervalMS != defaultCardUpdateIntervalMS {
		t.Errorf("absent -> %d, want default %d", c.cardUpdateIntervalMS, defaultCardUpdateIntervalMS)
	}
}

func TestParseConfig_ServiceURLAllowlist(t *testing.T) {
	// absent -> nil (allow any)
	c, _ := parseConfig(validOpts())
	if c.serviceURLAllowlist != nil {
		t.Errorf("absent service_url_allowlist -> %v, want nil", c.serviceURLAllowlist)
	}
	// comma-separated, trimmed, empties dropped
	opts := validOpts()
	opts["service_url_allowlist"] = " smba.trafficmanager.net , , smba.infra.gcc.teams.microsoft.com "
	c, err := parseConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"smba.trafficmanager.net", "smba.infra.gcc.teams.microsoft.com"}
	if len(c.serviceURLAllowlist) != len(want) {
		t.Fatalf("allowlist = %v, want %v", c.serviceURLAllowlist, want)
	}
	for i, h := range want {
		if c.serviceURLAllowlist[i] != h {
			t.Errorf("allowlist[%d] = %q, want %q", i, c.serviceURLAllowlist[i], h)
		}
	}
}

func TestServiceURLAllowed(t *testing.T) {
	list := []string{"smba.trafficmanager.net"}
	cases := []struct {
		name      string
		url       string
		allowlist []string
		want      bool
	}{
		{"empty allowlist allows any", "https://evil.example/x", nil, true},
		{"host matches (regional path)", "https://smba.trafficmanager.net/amer/v3/", list, true},
		{"host match is case-insensitive", "https://SMBA.TrafficManager.net/emea/", list, true},
		{"foreign host rejected", "https://attacker.example/v3/", list, false},
		{"unparseable url rejected when allowlist set", "://nope", list, false},
	}
	for _, tc := range cases {
		if got := serviceURLAllowed(tc.url, tc.allowlist); got != tc.want {
			t.Errorf("%s: serviceURLAllowed(%q) = %v, want %v", tc.name, tc.url, got, tc.want)
		}
	}
}

func TestParseConfig_CardLoadingText(t *testing.T) {
	// absent -> empty (label-less card, no built-in default)
	c, _ := parseConfig(validOpts())
	if c.cardLoadingText != "" {
		t.Errorf("absent card_loading_text -> %q, want empty", c.cardLoadingText)
	}
	// set -> used verbatim (trimmed)
	opts := validOpts()
	opts["card_loading_text"] = "  💭 Thinking…  "
	c, err := parseConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.cardLoadingText != "💭 Thinking…" {
		t.Errorf("card_loading_text = %q, want trimmed value", c.cardLoadingText)
	}
}

func TestParseConfig_MaxAttachmentBytes(t *testing.T) {
	// absent -> default
	c, _ := parseConfig(validOpts())
	if c.maxAttachmentBytes != defaultMaxAttachmentBytes {
		t.Errorf("absent max_attachment_bytes -> %d, want default %d", c.maxAttachmentBytes, defaultMaxAttachmentBytes)
	}
	// override + non-positive fallback
	cases := map[any]int64{
		int64(5 << 20): 5 << 20,
		1048576:        1048576,
		0:              defaultMaxAttachmentBytes,
		-1:             defaultMaxAttachmentBytes,
		"nope":         defaultMaxAttachmentBytes,
	}
	for in, want := range cases {
		opts := validOpts()
		opts["max_attachment_bytes"] = in
		c, err := parseConfig(opts)
		if err != nil {
			t.Fatalf("max_attachment_bytes %v: unexpected error: %v", in, err)
		}
		if c.maxAttachmentBytes != want {
			t.Errorf("max_attachment_bytes %v -> %d, want %d", in, c.maxAttachmentBytes, want)
		}
	}
}

func TestNew_RegistersAsPlatform(t *testing.T) {
	p, err := New(validOpts())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "teams" {
		t.Errorf("Name() = %q, want teams", p.Name())
	}
}
