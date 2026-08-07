package config

import (
	"strings"
	"testing"
)

// ============================================================
// Validate: 各渠道凭证必填校验
// ============================================================

// baseChannelConfig 构造一个带指定渠道条目的最小合法配置。
func baseChannelConfig(name string, entry ChannelEntry) *Config {
	entry.Enabled = true
	return &Config{
		LLM: LLMConfig{
			Providers: map[string]ProviderEntry{
				"p": {Protocol: "openai", BaseURL: "http://localhost:11434/v1"},
			},
		},
		Channels: map[string]ChannelEntry{name: entry},
	}
}

func TestValidate_Channel_MissingCredentials(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		entry   ChannelEntry
		missing string // 期望错误信息中包含的缺失字段
	}{
		{"webhook 缺 token", "webhook", ChannelEntry{}, "token"},
		{"dingtalk 缺 client_id", "dingtalk", ChannelEntry{ClientSecret: "s"}, "client_id"},
		{"feishu 缺 app_secret", "feishu", ChannelEntry{AppID: "a"}, "app_secret"},
		{"微信 缺 token", "微信", ChannelEntry{AppID: "a", AppSecret: "s"}, "token"},
		{"wecom 缺 agent_secret", "wecom", ChannelEntry{CorpID: "c", AgentID: "1", Token: "t"}, "agent_secret"},
		{"telegram 缺 token", "telegram", ChannelEntry{}, "token"},
		{"whatsapp 缺 token", "whatsapp", ChannelEntry{}, "token"},
		{"qq 缺 app_id", "qq", ChannelEntry{AppSecret: "s"}, "app_id"},
	}
	for _, tc := range cases {
		cfg := baseChannelConfig(tc.channel, tc.entry)
		err := cfg.Validate()
		if err == nil {
			t.Errorf("%s: 期望报错，实际通过", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.missing) {
			t.Errorf("%s: 错误信息应包含 %q，实际 %q", tc.name, tc.missing, err.Error())
		}
	}
}

func TestValidate_Channel_CompleteCredentials(t *testing.T) {
	cases := map[string]ChannelEntry{
		"webhook":  {Token: "t"},
		"dingtalk": {ClientID: "k", ClientSecret: "s"},
		"feishu":   {AppID: "a", AppSecret: "s"},
		"wechat":   {AppID: "a", AppSecret: "s", Token: "t"},
		"微信":       {AppID: "a", AppSecret: "s", Token: "t"},
		"wecom":    {CorpID: "c", AgentID: "1", AgentSecret: "s", Token: "t"},
		"telegram": {Token: "t"},
		"whatsapp": {Token: "t"},
		"qq":       {AppID: "a", AppSecret: "s"},
	}
	for name, entry := range cases {
		cfg := baseChannelConfig(name, entry)
		if err := cfg.Validate(); err != nil {
			t.Errorf("渠道 %q 凭证齐全应通过校验，实际报错: %v", name, err)
		}
	}
}

func TestValidate_Channel_DisabledSkipsCheck(t *testing.T) {
	cfg := baseChannelConfig("dingtalk", ChannelEntry{})
	cfg.Channels["dingtalk"] = ChannelEntry{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Errorf("未启用渠道不应校验凭证，实际报错: %v", err)
	}
}

func TestResolveEnvVars_ChannelSecrets(t *testing.T) {
	t.Setenv("CH_SECRET_TEST", "expanded-secret")
	cfg := &Config{
		Channels: map[string]ChannelEntry{
			"dingtalk": {ClientSecret: "${CH_SECRET_TEST}"},
			"feishu":   {AppSecret: "${CH_SECRET_TEST}", EncryptKey: "${CH_SECRET_TEST}"},
		},
	}
	cfg.resolveEnvVars()
	if cfg.Channels["dingtalk"].ClientSecret != "expanded-secret" {
		t.Errorf("client_secret 环境变量未展开: %q", cfg.Channels["dingtalk"].ClientSecret)
	}
	if cfg.Channels["feishu"].AppSecret != "expanded-secret" || cfg.Channels["feishu"].EncryptKey != "expanded-secret" {
		t.Errorf("feishu 密钥字段环境变量未展开: %+v", cfg.Channels["feishu"])
	}
}
