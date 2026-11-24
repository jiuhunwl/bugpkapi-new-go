package payment

import "testing"

func TestTrc20TokenAmount(t *testing.T) {
	cases := []struct {
		value    string
		decimals int
		want     string
		wantErr  bool
	}{
		{value: "5370000", decimals: 6, want: "5.370000"},
		{value: "1", decimals: 6, want: "0.000001"},
		{value: "0", decimals: 6, want: "0.000000"},
		{value: "100000000", decimals: 6, want: "100.000000"},
		{value: "abc", decimals: 6, wantErr: true},
		{value: "-1", decimals: 6, wantErr: true},
	}
	for _, tc := range cases {
		got, err := trc20TokenAmount(tc.value, tc.decimals)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("trc20TokenAmount(%q) expected error, got %q", tc.value, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("trc20TokenAmount(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
}

func TestNormalizeTrc20ConfigRequest(t *testing.T) {
	const validAddress = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

	cfg, apiKey, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{
		Enabled:       true,
		Address:       validAddress,
		APIKey:        "  trongrid-key  ",
		MinAmount:     "5",
		MaxAmount:     "500",
		Confirmations: 0,
	})
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.Address != validAddress || cfg.MinAmount != "5.000000" || cfg.MaxAmount != "500.000000" {
		t.Fatalf("normalized config = %+v", cfg)
	}
	if cfg.Confirmations != trc20DefaultConfirmations {
		t.Fatalf("confirmations = %d, want default %d", cfg.Confirmations, trc20DefaultConfirmations)
	}
	if apiKey != "trongrid-key" {
		t.Fatalf("api key = %q, want trimmed value", apiKey)
	}

	if _, _, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{Enabled: true}); err == nil {
		t.Fatal("enabled config without address was accepted")
	}
	if _, _, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{Enabled: true, Address: "bad"}); err == nil {
		t.Fatal("invalid address was accepted")
	}
	if _, _, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{MinAmount: "100", MaxAmount: "10"}); err == nil {
		t.Fatal("min greater than max was accepted")
	}
	if _, _, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{Confirmations: 99}); err == nil {
		t.Fatal("excessive confirmations were accepted")
	}
}

func TestTrc20Patterns(t *testing.T) {
	if !trc20AddressPattern.MatchString("TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t") {
		t.Fatal("valid TRON address rejected")
	}
	if trc20AddressPattern.MatchString("0x1234567890") {
		t.Fatal("invalid address accepted")
	}
	if !trc20TxIDPattern.MatchString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid txid rejected")
	}
	if trc20TxIDPattern.MatchString("xyz") {
		t.Fatal("invalid txid accepted")
	}
}

func TestTrc20SubmitCooldown(t *testing.T) {
	const orderNo = "trc20-cooldown-test-order"
	if !allowTrc20Submit(orderNo) {
		t.Fatal("first submit was rejected")
	}
	if allowTrc20Submit(orderNo) {
		t.Fatal("second submit inside cooldown was accepted")
	}
}
