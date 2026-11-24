package iphash

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// 未配置密钥时所有 SQL 前缀必须与历史公开字面盐逐字节一致 —— 这是
// 「存量 rollup 无需重建」的安全前提。
func TestUnconfiguredPrefixesMatchHistoricalLiterals(t *testing.T) {
	if got := RegionSQLPrefix(); got != "bugpk-region-ip-v1:" {
		t.Fatalf("RegionSQLPrefix() = %q, want historical literal", got)
	}
	if got := FailureSQLPrefix(); got != "bugpk-failure-ip-v1:" {
		t.Fatalf("FailureSQLPrefix() = %q, want historical literal", got)
	}
	if got := RiskSQLPrefix(); got != "bugpk-risk-ip-v1:" {
		t.Fatalf("RiskSQLPrefix() = %q, want historical literal", got)
	}
	if got := OperationLogSQLPrefix(); got != "bugpk-operation-log-v1:" {
		t.Fatalf("OperationLogSQLPrefix() = %q, want historical literal", got)
	}
}

// 配置密钥后，Go 侧摘要必须与 SQL 侧写入同一 IP 的
// unhex(sha2(concat('<prefix>', ip), 256)) 逐字节一致（风控查询键配对）。
func TestConfiguredRiskDigestMatchesSQLConstruction(t *testing.T) {
	defer Configure("")

	Configure("s3cr3t-key")
	const ip = "203.0.113.7"

	digest := RiskDigest(ip)
	want := sha256.Sum256([]byte(RiskSQLPrefix() + ip))
	if string(digest) != string(want[:]) {
		t.Fatal("RiskDigest must equal sha256(prefix||ip) as the SQL side constructs it")
	}

	// 前缀内联进单引号 SQL 字面量：密钥经 hex 编码后只能出现 [0-9a-f]，
	// 不可能破坏语法；且必须与未配置时的前缀不同（真正轮换了分组键）。
	prefix := RiskSQLPrefix()
	if strings.ContainsAny(prefix, "'\\") {
		t.Fatalf("SQL prefix must not contain quote characters: %q", prefix)
	}
	if !strings.HasSuffix(prefix, hex.EncodeToString([]byte("s3cr3t-key"))) {
		t.Fatalf("SQL prefix must end with hex-encoded secret: %q", prefix)
	}
	if prefix == Risk {
		t.Fatal("configured prefix must differ from the historical public prefix")
	}

	// 空白密钥视同未配置（回到历史前缀）。
	Configure("   ")
	if got := RegionSQLPrefix(); got != Region {
		t.Fatalf("blank secret must fall back to the historical prefix, got %q", got)
	}
}

// 同一 IP 在不同用途下前缀不同，保留「不可跨表关联」的原有性质。
func TestPurposePrefixesStayDistinct(t *testing.T) {
	defer Configure("")
	Configure("k")
	prefixes := map[string]bool{
		RegionSQLPrefix():       true,
		FailureSQLPrefix():      true,
		RiskSQLPrefix():         true,
		OperationLogSQLPrefix(): true,
	}
	if len(prefixes) != 4 {
		t.Fatalf("purpose prefixes must stay distinct, got %d unique values", len(prefixes))
	}
}

func TestNetPrefixNormalizesIPv4AndIPv6(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "203.0.113.7", want: "203.0.113.0/24"},
		{input: "203.0.113.255", want: "203.0.113.0/24"},
		{input: "::ffff:203.0.113.7", want: "203.0.113.0/24"},
		{input: "2001:db8:abcd:1234:5678:9abc:def0:1", want: "2001:db8:abcd:1234::/64"},
		{input: "not-an-ip", want: "not-an-ip"},
	} {
		if got := NetPrefix(test.input); got != test.want {
			t.Fatalf("NetPrefix(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestPrefixDigestIsStableWithinOneSubnet(t *testing.T) {
	defer Configure("")
	a := PrefixDigest("198.51.100.10")
	b := PrefixDigest("198.51.100.200")
	if string(a) != string(b) {
		t.Fatal("same /24 must produce the same prefix digest")
	}
	if string(a) == string(PrefixDigest("198.51.101.10")) {
		t.Fatal("different /24 must not collide")
	}
	if string(a) == string(RiskDigest("198.51.100.10")) {
		t.Fatal("prefix digest must not equal the full-IP digest")
	}
}
