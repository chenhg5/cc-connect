package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestProjectConfig_UsageLimitReply(t *testing.T) {
	var pc ProjectConfig
	if _, err := toml.Decode(`usage_limit_reply = "这边额度用完了，已转给飞书秘书处理"`, &pc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pc.UsageLimitReply != "这边额度用完了，已转给飞书秘书处理" {
		t.Fatalf("UsageLimitReply = %q", pc.UsageLimitReply)
	}
}
