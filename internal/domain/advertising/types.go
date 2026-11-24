package advertising

import "bugpk-api/internal/domain/rbac"

const (
	PlacementHome        = "HOME"
	PlacementConsole     = "CONSOLE"
	PlacementNotice      = "NOTICE"
	PlacementService     = "SERVICE_PROVIDER"
	PlacementAPIResponse = "API_RESPONSE"
)

type Principal = rbac.Principal

type Advertisement struct {
	CreateTime  string         `json:"createTime,omitempty"`
	Description string         `json:"description,omitempty"`
	EndTime     string         `json:"endTime,omitempty"`
	ID          string         `json:"id"`
	ImageURL    string         `json:"imageUrl,omitempty"`
	LinkTarget  string         `json:"linkTarget"`
	LinkURL     string         `json:"linkUrl,omitempty"`
	Name        string         `json:"name"`
	Payload     map[string]any `json:"payload,omitempty"`
	Placement   string         `json:"placement"`
	SortNo      int            `json:"sortNo"`
	StartTime   string         `json:"startTime,omitempty"`
	Status      int            `json:"status"`
	Title       string         `json:"title"`
	UpdateTime  string         `json:"updateTime,omitempty"`
}

// PublicAdvertisement is the public-safe web advertisement contract. Internal
// names, status, scheduling metadata and payload JSON must never be exposed by
// the unauthenticated advertisement endpoint.
type PublicAdvertisement struct {
	Description string `json:"description,omitempty"`
	ID          string `json:"id"`
	ImageURL    string `json:"imageUrl,omitempty"`
	LinkTarget  string `json:"linkTarget"`
	LinkURL     string `json:"linkUrl,omitempty"`
	Placement   string `json:"placement"`
	Title       string `json:"title"`
}

type SaveAdvertisementRequest struct {
	Description string         `json:"description"`
	EndTime     string         `json:"endTime"`
	ImageURL    string         `json:"imageUrl"`
	LinkTarget  string         `json:"linkTarget"`
	LinkURL     string         `json:"linkUrl"`
	Name        string         `json:"name"`
	Payload     map[string]any `json:"payload"`
	Placement   string         `json:"placement"`
	SortNo      int            `json:"sortNo"`
	StartTime   string         `json:"startTime"`
	Status      *int           `json:"status"`
	Title       string         `json:"title"`
}

type ListParams struct {
	Keyword   string
	Page      int
	PageSize  int
	Placement string
	Status    *int
}

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}
