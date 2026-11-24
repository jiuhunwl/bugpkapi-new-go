package content

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeDonationConfigSortsAndValidatesMethods(t *testing.T) {
	name := strings.Repeat("a", 32) + ".png"
	cfg, err := normalizeDonationConfig(SaveDonationConfigRequest{
		Description:     "Support the project",
		Enabled:         1,
		Eyebrow:         "Donate",
		ScanDescription: "Choose a channel",
		ScanTitle:       "Scan to donate",
		Thanks:          "Thank you",
		Title:           "Donation",
		Methods: []DonationMethod{
			{Key: "wechat", Name: "WeChat", Icon: "simple-icons:wechat", Color: "#07c160", QRCodeURL: "/system-api/upload/donation-qr/" + name, SortNo: 20, Enabled: 1},
			{Key: "alipay", Name: "Alipay", Icon: "lucide:circle-dollar-sign", Color: "#1677ff", QRCodeURL: "https://cdn.example.com/alipay.png", SortNo: 10, Enabled: 1},
		},
	}, "/system-api")
	if err != nil {
		t.Fatalf("normalizeDonationConfig() error=%v", err)
	}
	if len(cfg.Methods) != 2 || cfg.Methods[0].Key != "alipay" || cfg.Methods[1].Key != "wechat" {
		t.Fatalf("methods were not sorted: %#v", cfg.Methods)
	}
}

func TestNormalizeDonationConfigRejectsBrokenPublishedConfig(t *testing.T) {
	tests := []SaveDonationConfigRequest{
		{Enabled: 1, Eyebrow: "Donate", Title: "Donation", ScanTitle: "Scan", Methods: nil},
		{Enabled: 0, Eyebrow: "Donate", Title: "Donation", ScanTitle: "Scan", Methods: []DonationMethod{{Key: "Bad Key", Name: "X"}}},
		{Enabled: 0, Eyebrow: "Donate", Title: "Donation", ScanTitle: "Scan", Methods: []DonationMethod{{Key: "x", Name: "X", Color: "red"}}},
		{Enabled: 0, Eyebrow: "Donate", Title: "Donation", ScanTitle: "Scan", Methods: []DonationMethod{{Key: "x", Name: "X", QRCodeURL: "javascript:alert(1)"}}},
		{Enabled: 0, Eyebrow: "Donate", Title: "Donation", ScanTitle: "Scan", Methods: []DonationMethod{{Key: "x", Name: "X"}, {Key: "x", Name: "Duplicate"}}},
	}
	for _, test := range tests {
		if _, err := normalizeDonationConfig(test, "/system-api"); err == nil {
			t.Fatalf("expected invalid donation config to fail: %#v", test)
		}
	}
}

func TestPublicDonationConfigOnlyIncludesPublishedMethods(t *testing.T) {
	public := publicDonationConfig(DonationConfig{
		Enabled: 1,
		Methods: []DonationMethod{
			{Key: "alipay", Enabled: 1},
			{Key: "wechat", Enabled: 0},
		},
	})
	if len(public.Methods) != 1 || public.Methods[0].Key != "alipay" {
		t.Fatalf("unexpected public methods: %#v", public.Methods)
	}

	disabled := publicDonationConfig(DonationConfig{
		Enabled: 0,
		Methods: []DonationMethod{{Key: "alipay", Enabled: 1}},
	})
	if len(disabled.Methods) != 0 {
		t.Fatalf("disabled config exposed methods: %#v", disabled.Methods)
	}
}

func TestDefaultDonationConfigStartsUnpublished(t *testing.T) {
	cfg := defaultDonationConfig()
	if cfg.ID != 1 || cfg.Enabled != 0 || cfg.Methods == nil {
		t.Fatalf("unexpected default donation config: %#v", cfg)
	}
}

func TestNormalizeDonationSupporterTrimsAndFormatsFields(t *testing.T) {
	now := time.Date(2026, time.August, 10, 9, 8, 7, 0, time.FixedZone("expected business +08", 8*60*60))
	item, err := normalizeDonationSupporter(CreateDonationSupporterRequest{
		Amount:    " 0012.5 ",
		AvatarURL: " https://cdn.example.com/avatar.png ",
		DonatedAt: " 2026-08-09 08:30:00 ",
		Message:   " Thank you for the project ",
		Nickname:  " Alice ",
	}, now)
	if err != nil {
		t.Fatalf("normalizeDonationSupporter() error=%v", err)
	}
	if item.Nickname != "Alice" || item.AvatarURL != "https://cdn.example.com/avatar.png" {
		t.Fatalf("supporter identity was not normalized: %#v", item)
	}
	if item.Amount != "12.50" || item.Message != "Thank you for the project" {
		t.Fatalf("supporter details were not normalized: %#v", item)
	}
	if item.DonatedAt != "2026-08-09 08:30:00" {
		t.Fatalf("donatedAt=%q", item.DonatedAt)
	}
}

func TestNormalizeDonationSupporterDefaultsDonationTime(t *testing.T) {
	now := time.Date(2026, time.August, 10, 9, 8, 7, 0, time.FixedZone("expected business +08", 8*60*60))
	item, err := normalizeDonationSupporter(CreateDonationSupporterRequest{
		Amount:   "8",
		Nickname: "Bob",
	}, now)
	if err != nil {
		t.Fatalf("normalizeDonationSupporter() error=%v", err)
	}
	if item.Amount != "8.00" || item.DonatedAt != "2026-08-10 09:08:07" {
		t.Fatalf("unexpected defaults: %#v", item)
	}
}

func TestNormalizeDonationSupporterRejectsInvalidInput(t *testing.T) {
	now := time.Date(2026, time.August, 10, 9, 8, 7, 0, time.FixedZone("expected business +08", 8*60*60))
	tests := map[string]CreateDonationSupporterRequest{
		"blank nickname":    {Amount: "1", Nickname: "   "},
		"long nickname":     {Amount: "1", Nickname: strings.Repeat("a", 65)},
		"blank amount":      {Amount: "", Nickname: "Alice"},
		"zero amount":       {Amount: "0.00", Nickname: "Alice"},
		"negative amount":   {Amount: "-1", Nickname: "Alice"},
		"too many decimals": {Amount: "1.234", Nickname: "Alice"},
		"exponent amount":   {Amount: "1e3", Nickname: "Alice"},
		"oversized amount":  {Amount: "10000000000.00", Nickname: "Alice"},
		"unsafe avatar":     {Amount: "1", AvatarURL: "javascript:alert(1)", Nickname: "Alice"},
		"long message":      {Amount: "1", Message: strings.Repeat("a", 256), Nickname: "Alice"},
		"invalid donatedAt": {Amount: "1", DonatedAt: "2026/08/10", Nickname: "Alice"},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeDonationSupporter(request, now); err == nil {
				t.Fatalf("expected invalid supporter to fail: %#v", request)
			}
		})
	}
}
