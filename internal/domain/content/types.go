package content

import "bugpk-api/internal/domain/rbac"

type Principal = rbac.Principal

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type ListParams struct {
	IsHomeTicker *int
	IsPopup      *int
	IsTop        *int
	Keyword      string
	Page         int
	PageSize     int
	Status       *int
}

type NoticeItem struct {
	ContentHTML  string `json:"contentHtml,omitempty"`
	CreateTime   string `json:"createTime,omitempty"`
	CreatorName  string `json:"creatorName,omitempty"`
	ExpireTime   string `json:"expireTime,omitempty"`
	ID           string `json:"id"`
	IsHomeTicker int    `json:"isHomeTicker"`
	IsPopup      int    `json:"isPopup"`
	IsTop        int    `json:"isTop"`
	PublishTime  string `json:"publishTime,omitempty"`
	Status       int    `json:"status"`
	Summary      string `json:"summary"`
	Title        string `json:"title"`
	UpdateTime   string `json:"updateTime,omitempty"`
}

type SaveNoticeRequest struct {
	ContentHTML  string `json:"contentHtml"`
	ExpireTime   string `json:"expireTime"`
	IsHomeTicker *int   `json:"isHomeTicker"`
	IsPopup      *int   `json:"isPopup"`
	IsTop        *int   `json:"isTop"`
	Status       *int   `json:"status"`
	Title        string `json:"title"`
}

type ToggleRequest struct {
	IsPopup *int `json:"isPopup"`
	IsTop   *int `json:"isTop"`
	Status  *int `json:"status"`
}

// HomeTicker is the one line the public homepage marquee shows. It is derived
// from the notice marked is_home_ticker instead of a second announcement table.
type HomeTicker struct {
	Content    string `json:"content"`
	Enabled    int    `json:"enabled"`
	NoticeID   string `json:"noticeId,omitempty"`
	UpdateTime string `json:"updateTime,omitempty"`
}

// PopupNoticeResult separates what the daily popup can display from how many
// announcements are actually flagged, so the label never claims a number the
// response cannot back.
type PopupNoticeResult struct {
	Items []NoticeItem `json:"items"`
	Total int64        `json:"total"`
}

type SiteConfig struct {
	ContactAddress          string `json:"contactAddress,omitempty"`
	ContactEmail            string `json:"contactEmail,omitempty"`
	ContactPhone            string `json:"contactPhone,omitempty"`
	ContactQq               string `json:"contactQq,omitempty"`
	ContactWechat           string `json:"contactWechat,omitempty"`
	Copyright               string `json:"copyright,omitempty"`
	AnalyticsCode           string `json:"analyticsCode,omitempty"`
	Description             string `json:"description,omitempty"`
	ICP                     string `json:"icp,omitempty"`
	ID                      int64  `json:"id,omitempty"`
	GlobalLoadingPreset     string `json:"globalLoadingPreset"`
	LoadingAnimationEnabled int    `json:"loadingAnimationEnabled"`
	LoadingAnimationPreset  string `json:"loadingAnimationPreset"`
	LoadingAnimationURL     string `json:"loadingAnimationUrl,omitempty"`
	LogoURL                 string `json:"logoUrl,omitempty"`
	Noindex                 int    `json:"noindex"`
	OGImage                 string `json:"ogImage,omitempty"`
	SEOKeywords             string `json:"seoKeywords,omitempty"`
	SEOTitle                string `json:"seoTitle,omitempty"`
	SiteName                string `json:"siteName"`
	SiteURL                 string `json:"siteUrl,omitempty"`
	Slogan                  string `json:"slogan,omitempty"`
	// Open-source branding fields – managed by administrator in site config.
	GitHubURL   string `json:"githubUrl,omitempty"`
	AuthorName  string `json:"authorName,omitempty"`
	AuthorURL   string `json:"authorUrl,omitempty"`
	LicenseType string `json:"licenseType,omitempty"`
	LicenseURL  string `json:"licenseUrl,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
}

type FriendLinkItem struct {
	ApplicantEmail    string `json:"applicantEmail,omitempty"`
	ApplicantName     string `json:"applicantName,omitempty"`
	CreateTime        string `json:"createTime,omitempty"`
	Description       string `json:"description,omitempty"`
	ID                string `json:"id"`
	Language          string `json:"language,omitempty"`
	LogoURL           string `json:"logoUrl,omitempty"`
	NormalizedSiteURL string `json:"normalizedSiteUrl,omitempty"`
	SiteName          string `json:"siteName"`
	SiteURL           string `json:"siteUrl"`
	SortNo            int    `json:"sortNo"`
	Status            int    `json:"status"`
	UpdateTime        string `json:"updateTime,omitempty"`
	UserID            string `json:"userId,omitempty"`
}

// SubmitFriendLinkChangeRequest 用户对自己已上线友链提出的修改内容。
// 联系方式不在这里改：申请人邮箱是审核沟通通道，改它会让管理员找不到人。
type SubmitFriendLinkChangeRequest struct {
	BacklinkURL string `json:"backlinkUrl"`
	Description string `json:"description"`
	LogoURL     string `json:"logoUrl"`
	SiteName    string `json:"siteName"`
	SiteURL     string `json:"siteUrl"`
}

// MyFriendLinks 是用户「我的友链」面板：已上线的自有友链、自己的历史请求、
// 以及当前是否允许申请（沿用后台总开关，关掉时前端只隐藏「申请」按钮）。
type MyFriendLinks struct {
	ApplyEnabled bool                    `json:"applyEnabled"`
	Links        []FriendLinkItem        `json:"links"`
	Requests     []FriendLinkApplication `json:"requests"`
}

type SaveFriendLinkRequest struct {
	ApplicantEmail string `json:"applicantEmail"`
	ApplicantName  string `json:"applicantName"`
	Description    string `json:"description"`
	Language       string `json:"language"`
	LogoURL        string `json:"logoUrl"`
	SiteName       string `json:"siteName"`
	SiteURL        string `json:"siteUrl"`
	SortNo         int    `json:"sortNo"`
	Status         *int   `json:"status"`
}

type FriendLinkApplication struct {
	BacklinkURL          string `json:"backlinkUrl,omitempty"`
	ChangeKind           string `json:"changeKind,omitempty"`
	ContactEmail         string `json:"contactEmail,omitempty"`
	ContactName          string `json:"contactName,omitempty"`
	ContactQq            string `json:"contactQq,omitempty"`
	CreateTime           string `json:"createTime,omitempty"`
	Description          string `json:"description,omitempty"`
	ID                   string `json:"id"`
	Language             string `json:"language"`
	LinkID               string `json:"linkId,omitempty"`
	LogoURL              string `json:"logoUrl,omitempty"`
	NormalizedSiteURL    string `json:"normalizedSiteUrl,omitempty"`
	RejectReason         string `json:"rejectReason,omitempty"`
	ReviewReasonTemplate string `json:"reasonTemplate,omitempty"`
	ReviewSendEmail      bool   `json:"sendEmail"`
	ReviewTime           string `json:"reviewTime,omitempty"`
	ReviewerName         string `json:"reviewerName,omitempty"`
	SiteName             string `json:"siteName"`
	SiteURL              string `json:"siteUrl"`
	Status               string `json:"status"`
	UpdateTime           string `json:"updateTime,omitempty"`
	UserID               string `json:"userId"`
	Username             string `json:"username,omitempty"`
}

type ApplyFriendLinkRequest struct {
	BacklinkURL  string `json:"backlinkUrl"`
	CaptchaID    string `json:"captchaId"`
	ContactEmail string `json:"contactEmail"`
	ContactName  string `json:"contactName"`
	ContactQq    string `json:"contactQq"`
	Description  string `json:"description"`
	LogoURL      string `json:"logoUrl"`
	Language     string `json:"language"`
	SiteName     string `json:"siteName"`
	SiteURL      string `json:"siteUrl"`
}

type ReviewApplicationRequest struct {
	Reason         string `json:"reason"`
	ReasonTemplate string `json:"reasonTemplate"`
	RejectReason   string `json:"rejectReason"`
	SendEmail      *bool  `json:"sendEmail"`
}

type RejectApplicationRequest = ReviewApplicationRequest
type ApproveApplicationRequest = ReviewApplicationRequest
type DeleteFriendLinkRequest = ReviewApplicationRequest

type FriendLinkConfig struct {
	ApplyEnabled int    `json:"applyEnabled"`
	ApplyNotice  string `json:"applyNotice,omitempty"`
	ID           int64  `json:"id,omitempty"`
	UpdateTime   string `json:"updateTime,omitempty"`
}

type ApplyStatus struct {
	CreateTime string `json:"createTime,omitempty"`
	Status     string `json:"status,omitempty"`
	Submitted  bool   `json:"submitted"`
}

type PublicMeta struct {
	Config   FriendLinkConfig `json:"config"`
	SiteInfo SiteConfig       `json:"siteInfo"`
}

type HomeOverview struct {
	HomeNotice HomeTicker    `json:"homeNotice"`
	HotAPIs    []HotAPIItem  `json:"hotApis"`
	SiteConfig SiteConfig    `json:"siteConfig"`
	Stats      PlatformStats `json:"stats"`
}

// HomeAnalyticsSnapshot contains only the data that changes with gateway
// traffic. Site branding and the home notice stay outside the snapshot so
// content changes remain visible immediately without rebuilding analytics.
type HomeAnalyticsSnapshot struct {
	HotAPIs []HotAPIItem  `json:"hotApis"`
	Stats   PlatformStats `json:"stats"`
	// CoveredUpTo records the newest call-log timestamp this payload included
	// when the background refresh wrote it. Readers use it to prove how far the
	// published numbers reach instead of assuming they are current.
	CoveredUpTo string `json:"coveredUpTo,omitempty"`
}

type PlatformStats struct {
	ActiveRegions24h      int64  `json:"activeRegions24h"`
	ActiveRegions24hDelta int64  `json:"activeRegions24hDelta"`
	APITotal              int64  `json:"apiTotal"`
	AvailabilityPercent   string `json:"availabilityPercent"`
	AvailabilitySource    string `json:"availabilitySource"`
	Calls24h              int64  `json:"calls24h"`
	Calls24hDeltaPercent  string `json:"calls24hDeltaPercent"`
	EnabledAPITotal       int64  `json:"enabledApiTotal"`
	PeakQPS24h            int64  `json:"peakQps24h"`
	PeakQPS24hDelta       int64  `json:"peakQps24hDelta"`
	UserTotal             int64  `json:"userTotal"`

	// The truth envelope below is stamped at read time, never persisted. A
	// client must treat analyticsReady=false as "the traffic fields carry no
	// information" and render UnavailableFields as unavailable rather than as
	// zeros. See analytics_health.go for the decision table.
	AnalyticsReady    bool     `json:"analyticsReady"`
	DegradedSections  []string `json:"degradedSections"`
	UnavailableFields []string `json:"unavailableFields"`
	CoveredUpTo       string   `json:"coveredUpTo,omitempty"`
	LagSeconds        int64    `json:"lagSeconds"`
}

type HotAPIItem struct {
	AvatarURL     string `json:"avatarUrl,omitempty"`
	CallCount     int64  `json:"callCount"`
	Description   string `json:"description,omitempty"`
	ID            string `json:"id"`
	IsFeatured    int    `json:"isFeatured,omitempty"`
	IsTop         int    `json:"isTop,omitempty"`
	Name          string `json:"name"`
	PointPrice    int64  `json:"pointPrice,omitempty"`
	Price         string `json:"price,omitempty"`
	RequestMethod string `json:"requestMethod,omitempty"`
}
