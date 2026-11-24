package ipconfig

// Config 是 IP 归属地数据源的后台配置（对应 bp_ip_config 单点表 id=1）。
type Config struct {
	DataSourceName string `json:"dataSourceName"`
	APIURL         string `json:"apiUrl"`
	ParseRule      string `json:"parseRule"`
	Enabled        int    `json:"enabled"`
	Remark         string `json:"remark,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
}

// TestSourceRequest validates a custom provider without persisting it.
// FieldMappings uses the platform field name as key and a JSON path as value.
type TestSourceRequest struct {
	APIURL        string            `json:"apiUrl"`
	FieldMappings map[string]string `json:"fieldMappings"`
	TestIP        string            `json:"testIp"`
}

type SourceFieldOption struct {
	Path         string `json:"path"`
	ValuePreview string `json:"valuePreview"`
	ValueType    string `json:"valueType"`
}

type SourcePreview struct {
	City      string   `json:"city"`
	Country   string   `json:"country"`
	ISP       string   `json:"isp"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Province  string   `json:"province"`
}

type TestSourceResult struct {
	Fields            []SourceFieldOption `json:"fields"`
	InvalidFields     []string            `json:"invalidFields"`
	MissingPairFields []string            `json:"missingPairFields"`
	Preview           SourcePreview       `json:"preview"`
}
