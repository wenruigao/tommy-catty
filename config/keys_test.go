// keys_test.go 验证配置键白名单、类型校验、脱敏与建议。
package config

import (
	"testing"
)

// TestValidateValue_Kinds 验证各类型的校验行为。
func TestValidateValue_Kinds(t *testing.T) {
	intSpec, ok := LookupKey("engine.max_iterations")
	if !ok {
		t.Fatal("engine.max_iterations 应存在")
	}
	if err := ValidateValue(intSpec, "abc"); err == nil {
		t.Error("非整数应报错")
	}
	if err := ValidateValue(intSpec, "0"); err == nil {
		t.Error("max_iterations 为 0 应报错（必须正整数）")
	}
	if err := ValidateValue(intSpec, "30"); err != nil {
		t.Errorf("合法整数不应报错: %v", err)
	}

	boolSpec, _ := LookupKey("llm.cache.enabled")
	if err := ValidateValue(boolSpec, "yes"); err == nil {
		t.Error("非 true/false 应报错")
	}

	durSpec, _ := LookupKey("session.ttl")
	if err := ValidateValue(durSpec, "30x"); err == nil {
		t.Error("非法时长应报错")
	}
	if err := ValidateValue(durSpec, "30m"); err != nil {
		t.Errorf("合法时长不应报错: %v", err)
	}

	enumSpec, _ := LookupKey("search.default_engine")
	if err := ValidateValue(enumSpec, "google"); err == nil {
		t.Error("超出枚举范围应报错")
	}
	if err := ValidateValue(enumSpec, "tavily"); err != nil {
		t.Errorf("合法枚举值不应报错: %v", err)
	}
}

// TestSet_DefaultProviderExistence 验证供应商存在性校验。
func TestSet_DefaultProviderExistence(t *testing.T) {
	spec, _ := LookupKey("llm.default_provider")
	cfg := &Config{}
	cfg.LLM.Providers = map[string]ProviderEntry{
		"mimo": {Model: "m"},
		"qwen": {Model: "q"},
	}

	if err := spec.Set(cfg, "not-exist"); err == nil {
		t.Error("不存在的供应商应报错")
	}
	if err := spec.Set(cfg, "qwen"); err != nil {
		t.Errorf("已存在的供应商不应报错: %v", err)
	}
	if cfg.LLM.DefaultProvider != "qwen" {
		t.Errorf("内存配置应更新为 qwen，得到 %q", cfg.LLM.DefaultProvider)
	}
}

// TestLookupProviderKey 验证动态键解析与字段白名单。
func TestLookupProviderKey(t *testing.T) {
	for _, field := range ProviderFields {
		if spec := LookupProviderKey("llm.providers.qwen." + field); spec == nil {
			t.Errorf("字段 %s 应可解析", field)
		}
	}
	if spec := LookupProviderKey("llm.providers.qwen.protocol"); spec != nil {
		t.Error("非白名单字段应返回 nil")
	}
	if spec := LookupProviderKey("llm.providers..model"); spec != nil {
		t.Error("空供应商名应返回 nil")
	}
	if spec := LookupProviderKey("llm.qwen.model"); spec != nil {
		t.Error("非 providers 路径应返回 nil")
	}

	// api_key 动态键标记为 Secret
	spec := LookupProviderKey("llm.providers.qwen.api_key")
	if spec == nil || !spec.Secret {
		t.Error("api_key 动态键应标记 Secret")
	}
}

// TestProviderKey_GetSet 验证动态键对 Config 的读写。
func TestProviderKey_GetSet(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Providers = map[string]ProviderEntry{"qwen": {Model: "old"}}

	spec, ok := LookupKey("llm.providers.qwen.model")
	if !ok {
		t.Fatal("动态键应可通过 LookupKey 找到")
	}
	if err := spec.Set(cfg, "qwen-max"); err != nil {
		t.Fatalf("设置失败: %v", err)
	}
	if got := spec.Get(cfg); got != "qwen-max" {
		t.Errorf("读取应为 qwen-max，得到 %q", got)
	}

	// 不存在的供应商
	spec2, _ := LookupKey("llm.providers.other.model")
	if err := spec2.Set(cfg, "x"); err == nil {
		t.Error("供应商不存在时 Set 应报错")
	}
}

// TestSuggestKey 验证未知键的相近建议。
func TestSuggestKey(t *testing.T) {
	if got := SuggestKey("llm.default_provide"); got != "llm.default_provider" {
		t.Errorf("应建议 llm.default_provider，得到 %q", got)
	}
	if got := SuggestKey("xyz"); got != "" {
		t.Errorf("无相近键时应返回空串，得到 %q", got)
	}
}

// TestMaskValue 验证脱敏规则。
func TestMaskValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "(未设置)"},
		{"${MIMO_API_KEY}", "${MIMO_API_KEY}"},
		{"abc", "***"},
		{"sk-1234567890abcdef", "sk-***ef"},
	}
	for _, c := range cases {
		if got := MaskValue(c.in); got != c.want {
			t.Errorf("MaskValue(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestIsSecretKey 验证秘密键判定。
func TestIsSecretKey(t *testing.T) {
	for _, k := range []string{"llm.providers.x.api_key", "channels.webhook.token", "databases.a.dsn"} {
		if !IsSecretKey(k) {
			t.Errorf("%s 应判定为秘密键", k)
		}
	}
	if IsSecretKey("engine.max_iterations") {
		t.Error("engine.max_iterations 不应判定为秘密键")
	}
}

// TestParseNative 验证值类型转换。
func TestParseNative(t *testing.T) {
	intSpec, _ := LookupKey("engine.max_iterations")
	if v := ParseNative(intSpec, "42"); v != 42 {
		t.Errorf("int 转换应为 42，得到 %v (%T)", v, v)
	}
	boolSpec, _ := LookupKey("llm.cache.enabled")
	if v := ParseNative(boolSpec, "true"); v != true {
		t.Errorf("bool 转换应为 true，得到 %v", v)
	}
	strSpec, _ := LookupKey("work_dir")
	if v := ParseNative(strSpec, "/tmp"); v != "/tmp" {
		t.Errorf("string 应保持原值，得到 %v", v)
	}
}

// TestKeyRegistry_SecretMarked 注册表中秘密键必须标记 Secret，且每键有说明。
func TestKeyRegistry_SecretMarked(t *testing.T) {
	for _, spec := range AllKeySpecs() {
		if IsSecretKey(spec.Key) && !spec.Secret {
			t.Errorf("秘密键 %s 未标记 Secret", spec.Key)
		}
		if spec.Desc == "" {
			t.Errorf("键 %s 缺少说明", spec.Key)
		}
		if spec.Get == nil || spec.Set == nil {
			t.Errorf("键 %s 缺少 Get/Set 函数", spec.Key)
		}
	}
}
