//go:build darwin

package sandbox

import (
	"strings"
	"testing"
)

// TestSeatbeltProfile_DenyNetwork 验证默认禁网的 Seatbelt profile 生成。
func TestSeatbeltProfile_DenyNetwork(t *testing.T) {
	p := seatbeltProfile(Config{}, "/tmp/work")
	for _, want := range []string{
		"(version 1)",
		"(deny default)",
		"(allow process-exec*)",
		"(allow process-fork)",
		"(allow file-read*)",
		`(subpath "/tmp/work")`,
		`(subpath "/private/tmp")`,
		"(deny network*)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile 缺少 %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "(allow network*)") {
		t.Errorf("默认应禁网:\n%s", p)
	}
}

// TestSeatbeltProfile_AllowNet 验证 allow_net=true 时放行网络。
func TestSeatbeltProfile_AllowNet(t *testing.T) {
	p := seatbeltProfile(Config{AllowNet: true}, "/tmp/work")
	if !strings.Contains(p, "(allow network*)") {
		t.Errorf("allow_net=true 应含 allow network*:\n%s", p)
	}
}

// TestSeatbeltProfile_Escape 验证路径特殊字符转义。
func TestSeatbeltProfile_Escape(t *testing.T) {
	p := seatbeltProfile(Config{}, `/tmp/we"ird\path`)
	if !strings.Contains(p, `(subpath "/tmp/we\"ird\\path")`) {
		t.Errorf("路径转义失败:\n%s", p)
	}
}

// TestNativeAvailable_Darwin 验证 darwin 上 sandbox-exec 探测（本机应可用）。
func TestNativeAvailable_Darwin(t *testing.T) {
	if err := nativeAvailable(Config{}); err != nil {
		t.Skipf("本机无 sandbox-exec，跳过: %v", err)
	}
}
