package payment

import "testing"

func TestOrderExpireMinutesByChannel(t *testing.T) {
	cases := map[string]int{
		ChannelHupijiaoAlipay: hupijiaoOrderExpireMinutes,
		ChannelHupijiaoWechat: hupijiaoOrderExpireMinutes,
		ChannelAlipay:         defaultOrderExpireMinutes,
		ChannelWechat:         defaultOrderExpireMinutes,
		"":                    defaultOrderExpireMinutes,
	}
	for channel, want := range cases {
		if got := orderExpireMinutes(channel); got != want {
			t.Fatalf("orderExpireMinutes(%q) = %d, want %d", channel, got, want)
		}
	}
}

func TestPackageStockTableMapping(t *testing.T) {
	cases := map[string]string{
		"GLOBAL_PACKAGE":    "bp_package_global",
		"INTERFACE_PACKAGE": "bp_package_interface_spec",
		"POINT_PACKAGE":     "bp_package_point",
		"RECHARGE":          "",
	}
	for orderType, want := range cases {
		if got := packageStockTable(orderType); got != want {
			t.Fatalf("packageStockTable(%q) = %q, want %q", orderType, got, want)
		}
	}
}
