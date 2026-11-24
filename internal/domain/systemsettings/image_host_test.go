package systemsettings

import (
	"testing"

	"bugpk-api/internal/pkg/imagehost"
)

func TestNormalizeImageHostConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := normalizeImageHostConfig(imagehost.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled != 0 || cfg.Provider != imagehost.ProviderCloudflareImgBed || cfg.UploadPath != "/upload" || cfg.MaxBytes != imagehost.DefaultMaxBytes {
		t.Fatalf("defaults = %+v", cfg)
	}

	cfg, err = normalizeImageHostConfig(imagehost.Config{
		BaseURL:       "https://img.example.com/",
		Enabled:       1,
		Folder:        "/tickets/2026/",
		MaxBytes:      2 << 20,
		UploadChannel: "s3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://img.example.com" || cfg.Folder != "tickets/2026" || cfg.MaxBytes != 2<<20 {
		t.Fatalf("normalized = %+v", cfg)
	}

	for name, req := range map[string]imagehost.Config{
		"missing base":    {Enabled: 1},
		"bad scheme":      {BaseURL: "ftp://img.example.com", Enabled: 1},
		"query in base":   {BaseURL: "https://img.example.com?x=1", Enabled: 1},
		"bad channel":     {BaseURL: "https://img.example.com", Enabled: 1, UploadChannel: "nope"},
		"bad folder":      {BaseURL: "https://img.example.com", Enabled: 1, Folder: "../escape"},
		"bad provider":    {BaseURL: "https://img.example.com", Enabled: 1, Provider: "other"},
		"bad upload path": {BaseURL: "https://img.example.com", Enabled: 1, UploadPath: "/upload?x=1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeImageHostConfig(req); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}

	cfg, err = normalizeImageHostConfig(imagehost.Config{BaseURL: "https://img.example.com", Enabled: 1, MaxBytes: 99 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBytes != imagehost.DefaultMaxBytes {
		t.Fatalf("out-of-range max bytes = %d", cfg.MaxBytes)
	}
}
