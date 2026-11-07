package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestEffectiveFooterUsage_TOMLPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, text       string
		want5h, wantWeek bool
	}{
		{"defaults", "[[projects]]\nname='test'", false, false},
		{"global", "[display]\nshow_usage_5h=true\nshow_usage_week=true\n[[projects]]\nname='test'", true, true},
		{"project display", "[display]\nshow_usage_5h=true\nshow_usage_week=false\n[[projects]]\nname='test'\n[projects.display]\nshow_usage_5h=false\nshow_usage_week=true", false, true},
		{"direct overrides display", "[display]\nshow_usage_5h=false\nshow_usage_week=true\n[[projects]]\nname='test'\nshow_usage_5h=true\nshow_usage_week=false\n[projects.display]\nshow_usage_5h=false\nshow_usage_week=true", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if _, err := toml.Decode(tc.text, &cfg); err != nil {
				t.Fatal(err)
			}
			five, week := EffectiveFooterUsage(&cfg, &cfg.Projects[0])
			if five != tc.want5h || week != tc.wantWeek {
				t.Fatalf("effective quotas = %v/%v, want %v/%v", five, week, tc.want5h, tc.wantWeek)
			}
		})
	}
}

func TestSaveProjectSettings_FooterQuotaRoundTrip(t *testing.T) {
	path := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, path)
	show, hide := true, false
	for _, tc := range []struct{ five, week *bool }{{&show, &hide}, {&hide, &show}} {
		if err := SaveProjectSettings("alpha", ProjectSettingsUpdate{ShowUsage5h: tc.five, ShowUsageWeek: tc.week}); err != nil {
			t.Fatal(err)
		}
		cfg := readConfigFixture(t, path)
		five, week := EffectiveFooterUsage(cfg, &cfg.Projects[0])
		if five != *tc.five || week != *tc.week {
			t.Fatalf("saved quotas = %v/%v", five, week)
		}
		details := GetProjectConfigDetails("alpha")
		if details["show_usage_5h"] != *tc.five || details["show_usage_week"] != *tc.week {
			t.Fatalf("config details = %#v", details)
		}
	}
}
