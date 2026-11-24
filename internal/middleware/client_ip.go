package middleware

import (
	"fmt"
	"net"
	"strings"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TrustedProxyClientIP(values []string) (hertzapp.ClientIP, error) {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			_, network, err := net.ParseCIDR(value)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", value, err)
			}
			ones, _ := network.Mask.Size()
			if ones == 0 {
				return nil, fmt.Errorf("trusted proxy CIDR %q cannot trust an entire address family", value)
			}
			networks = append(networks, network)
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy IP %q", value)
		}
		bits := 128
		if ip.To4() != nil {
			ip = ip.To4()
			bits = 32
		}
		networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return hertzapp.ClientIPWithOption(hertzapp.ClientIPOptions{
		RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
		TrustedCIDRs:    networks,
	}), nil
}
