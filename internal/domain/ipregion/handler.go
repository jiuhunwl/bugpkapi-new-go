package ipregion

import (
	"context"

	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

// Handler 暴露与 IP 归属地相关的公开接口（如控制台首页定位）。
type Handler struct {
	resolver *Resolver
}

func NewHandler(resolver *Resolver) *Handler {
	return &Handler{resolver: resolver}
}

// GeoIPResult 是 GET /system-api/geoip 的响应结构。
type GeoIPResult struct {
	IP        string   `json:"ip"`
	Resolved  bool     `json:"resolved"`
	Status    string   `json:"status"`
	Country   string   `json:"country"`
	Province  string   `json:"province"`
	City      string   `json:"city"`
	Region    string   `json:"region"`
	ISP       string   `json:"isp"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Provider  string   `json:"provider,omitempty"`
}

// GeoIP 从当前请求 IP 解析归属地；失败或内网 IP 时返回空地区（前端显示“地区未知”）。
func (h *Handler) GeoIP(ctx context.Context, c *hertzapp.RequestContext) {
	clientIP := c.ClientIP()
	response.OK(c, h.geoIPResult(ctx, clientIP))
}

func (h *Handler) geoIPResult(ctx context.Context, clientIP string) GeoIPResult {
	result := GeoIPResult{IP: clientIP, Status: "unknown"}
	normalized := normalizeIP(clientIP)
	if normalized == "" {
		result.Status = "invalid"
		return result
	}
	result.IP = normalized
	if isPrivateIP(normalized) {
		result.Status = "private"
		return result
	}
	if h.resolver == nil || !h.resolver.cfg.Enabled {
		result.Status = "unavailable"
		return result
	}
	region, err := h.resolver.Resolve(ctx, normalized)
	if err != nil {
		result.Status = "unavailable"
		return result
	}
	if !hasRegionData(region) {
		return result
	}
	result.Resolved = true
	result.Status = "resolved"
	result.Country = region.Country
	result.Province = region.Province
	result.City = region.City
	result.Region = defaultText(region.Region, joinRegion(region.Country, region.Province, region.City))
	result.ISP = region.ISP
	result.Provider = region.Provider
	if validCoordinates(region.Latitude, region.Longitude) {
		latitude, longitude := region.Latitude, region.Longitude
		result.Latitude = &latitude
		result.Longitude = &longitude
	}
	return result
}
