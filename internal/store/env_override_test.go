package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOverridesSmartConfig(t *testing.T) {
	// 设置环境变量
	os.Setenv("TSM_HUB_SMART_STRATEGY", "stability_first")
	os.Setenv("TSM_HUB_SMART_COST_WEIGHT", "40")
	os.Setenv("TSM_HUB_SMART_STABILITY_WEIGHT", "100")
	os.Setenv("TSM_HUB_SMART_LATENCY_WEIGHT", "60")
	defer func() {
		os.Unsetenv("TSM_HUB_SMART_STRATEGY")
		os.Unsetenv("TSM_HUB_SMART_COST_WEIGHT")
		os.Unsetenv("TSM_HUB_SMART_STABILITY_WEIGHT")
		os.Unsetenv("TSM_HUB_SMART_LATENCY_WEIGHT")
	}()

	// 创建临时目录
	tmpDir := t.TempDir()

	// 创建 store
	st, err := New(filepath.Join(tmpDir, "config.json"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// 验证 smart 配置
	smart := st.Settings().Smart
	if smart.StrategyMode != "stability_first" {
		t.Errorf("StrategyMode = %q, want %q", smart.StrategyMode, "stability_first")
	}
	if smart.CostWeight != 40 {
		t.Errorf("CostWeight = %d, want %d", smart.CostWeight, 40)
	}
	if smart.StabilityWeight != 100 {
		t.Errorf("StabilityWeight = %d, want %d", smart.StabilityWeight, 100)
	}
	if smart.LatencyWeight != 60 {
		t.Errorf("LatencyWeight = %d, want %d", smart.LatencyWeight, 60)
	}

	t.Logf("Smart config: strategy=%s, cost=%d, stability=%d, latency=%d",
		smart.StrategyMode, smart.CostWeight, smart.StabilityWeight, smart.LatencyWeight)
}

func TestEnvOverridesLegacyPrefix(t *testing.T) {
	// 测试旧前缀兼容
	os.Setenv("LLM_ROUTER_SMART_STRATEGY", "balanced")
	defer os.Unsetenv("LLM_ROUTER_SMART_STRATEGY")

	tmpDir := t.TempDir()
	st, err := New(filepath.Join(tmpDir, "config.json"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	smart := st.Settings().Smart
	if smart.StrategyMode != "balanced" {
		t.Errorf("StrategyMode = %q, want %q", smart.StrategyMode, "balanced")
	}
}
