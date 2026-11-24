package systemsettings

import "testing"

func TestNormalizeOpenConfigKeepsRejectAboveQuarantine(t *testing.T) {
	cfg := normalizeOpenConfig(RegisterOpenConfig{
		RegisterRiskEnabled:         1,
		RegisterRiskQuarantineScore: 80,
		RegisterRiskRejectScore:     10,
	})
	if cfg.RegisterRiskRejectScore <= cfg.RegisterRiskQuarantineScore {
		t.Fatalf("reject score must stay above quarantine: %+v", cfg)
	}
	if cfg.RegisterRiskQuarantineScore != 80 || cfg.RegisterRiskRejectScore != 81 {
		t.Fatalf("unexpected threshold adjustment: %+v", cfg)
	}
}

func TestNormalizeOpenConfigClampsNewAccountAndDeviceLimits(t *testing.T) {
	cfg := normalizeOpenConfig(RegisterOpenConfig{
		NewAccountDayLimit:          -1,
		RegisterDeviceDayLimit:      -1,
		RegisterIPPrefixDayLimit:    -1,
		RegisterRiskQuarantineScore: 999,
		RegisterRiskRejectScore:     999,
	})
	if cfg.NewAccountDayLimit != 0 || cfg.RegisterDeviceDayLimit != 5 || cfg.RegisterIPPrefixDayLimit != 10 {
		t.Fatalf("unexpected registration limit defaults: %+v", cfg)
	}
	// 超出范围的评分回退到默认阈值，且拒绝阈值仍高于隔离阈值。
	if cfg.RegisterRiskRejectScore <= cfg.RegisterRiskQuarantineScore {
		t.Fatalf("reject threshold must stay above quarantine: %+v", cfg)
	}
	if cfg.RegisterRiskQuarantineScore != 40 || cfg.RegisterRiskRejectScore != 75 {
		t.Fatalf("out-of-range thresholds should fall back to defaults: %+v", cfg)
	}
}
