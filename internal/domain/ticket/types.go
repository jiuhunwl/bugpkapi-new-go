package ticket

import (
	"mime/multipart"

	"bugpk-api/internal/domain/rbac"
)

const (
	CategoryGeneral = "GENERAL"
	CategoryAPI     = "API"
	CategoryBilling = "BILLING"
	CategoryAccount = "ACCOUNT"
	CategoryBug     = "BUG"
	CategoryOther   = "OTHER"

	PriorityLow    = "LOW"
	PriorityNormal = "NORMAL"
	PriorityHigh   = "HIGH"
	PriorityUrgent = "URGENT"

	StatusOpen        = "OPEN"
	StatusProcessing  = "PROCESSING"
	StatusWaitingUser = "WAITING_USER"
	StatusResolved    = "RESOLVED"
	StatusClosed      = "CLOSED"

	SenderUser  = "USER"
	SenderStaff = "STAFF"

	ActivityPublic   = "PUBLIC"
	ActivityInternal = "INTERNAL"

	AttachmentStorageLocal = "local"
)

type Principal = rbac.Principal

type Ticket struct {
	AssignedTo         string   `json:"assignedTo,omitempty"`
	AssigneeName       string   `json:"assigneeName,omitempty"`
	Category           string   `json:"category"`
	CreatedAt          string   `json:"createdAt"`
	DisplayName        string   `json:"displayName,omitempty"`
	FirstRespondedAt   string   `json:"firstRespondedAt,omitempty"`
	FirstResponseDueAt string   `json:"firstResponseDueAt,omitempty"`
	FirstResponseLate  bool     `json:"firstResponseOverdue"`
	ID                 string   `json:"id"`
	Language           string   `json:"language"`
	LastMessageAt      string   `json:"lastMessageAt"`
	LastMessagePreview string   `json:"lastMessagePreview"`
	LastReplyBy        string   `json:"lastReplyBy"`
	MessageCount       int      `json:"messageCount"`
	Priority           string   `json:"priority"`
	ResolutionDueAt    string   `json:"resolutionDueAt,omitempty"`
	ResolutionLate     bool     `json:"resolutionOverdue"`
	ResolvedAt         string   `json:"resolvedAt,omitempty"`
	ClosedAt           string   `json:"closedAt,omitempty"`
	SLAStatus          string   `json:"slaStatus"`
	Status             string   `json:"status"`
	Subject            string   `json:"subject"`
	Tags               []string `json:"tags"`
	TicketNo           string   `json:"ticketNo"`
	UnreadCount        int      `json:"unreadCount"`
	UpdatedAt          string   `json:"updatedAt"`
	UserID             string   `json:"userId"`
	Username           string   `json:"username,omitempty"`
}

type Message struct {
	Content    string `json:"content"`
	CreatedAt  string `json:"createdAt"`
	ID         string `json:"id"`
	Internal   bool   `json:"internalNote"`
	SenderID   string `json:"senderId"`
	SenderName string `json:"senderName"`
	SenderType string `json:"senderType"`
}

// UserProfile is the compact account card shown when an administrator clicks an
// avatar inside a ticket. Email and mobile stay masked because the ticket
// permission does not imply the broader user-administration permission.
type UserProfile struct {
	Avatar        string   `json:"avatar,omitempty"`
	Balance       string   `json:"balance"`
	CreateTime    string   `json:"createTime,omitempty"`
	DisplayName   string   `json:"displayName,omitempty"`
	Email         string   `json:"email,omitempty"`
	ID            string   `json:"id"`
	LastLoginTime string   `json:"lastLoginTime,omitempty"`
	Mobile        string   `json:"mobile,omitempty"`
	Points        int64    `json:"points"`
	Roles         []string `json:"roles"`
	Status        int      `json:"status"`
	Username      string   `json:"username"`
}

type Detail struct {
	Activities  []Activity   `json:"activities"`
	Attachments []Attachment `json:"attachments"`
	Messages    []Message    `json:"messages"`
	Ticket      Ticket       `json:"ticket"`
}

type CreateRequest struct {
	Category string `json:"category"`
	Content  string `json:"content"`
	Language string `json:"language"`
	Priority string `json:"priority"`
	Subject  string `json:"subject"`
}

type ReplyRequest struct {
	AttachmentIDs []string `json:"attachmentIds"`
	Content       string   `json:"content"`
}

type UpdateRequest struct {
	AssignedTo *string   `json:"assignedTo"`
	Status     *string   `json:"status"`
	Tags       *[]string `json:"tags"`
}

type InternalNoteRequest struct {
	AttachmentIDs []string `json:"attachmentIds"`
	Content       string   `json:"content"`
}

type MarkReadRequest struct {
	LastMessageAt string `json:"lastMessageAt"`
	MessageCount  int    `json:"messageCount"`
}

type ListParams struct {
	AssignedTo  int64
	Category    string
	CreatedFrom string
	CreatedTo   string
	Keyword     string
	Overdue     bool
	Page        int
	PageSize    int
	Priority    string
	SortBy      string
	SortOrder   string
	Status      string
	Unread      bool
	UpdatedFrom string
	UpdatedTo   string
}

type Summary struct {
	All         int64 `json:"all"`
	Closed      int64 `json:"closed"`
	Open        int64 `json:"open"`
	Processing  int64 `json:"processing"`
	Resolved    int64 `json:"resolved"`
	Unread      int64 `json:"unread"`
	WaitingUser int64 `json:"waitingUser"`
}

type PageResult struct {
	Items   []Ticket `json:"items"`
	Summary Summary  `json:"summary"`
	Total   int64    `json:"total"`
}

type Attachment struct {
	ContentType  string `json:"contentType"`
	CreatedAt    string `json:"createdAt"`
	DownloadURL  string `json:"downloadUrl"`
	ID           string `json:"id"`
	MessageID    string `json:"messageId,omitempty"`
	OriginalName string `json:"originalName"`
	SizeBytes    int64  `json:"sizeBytes"`
	Storage      string `json:"storage"`
	UploaderID   string `json:"uploaderId"`
	UploaderName string `json:"uploaderName"`
	URL          string `json:"url,omitempty"`
}

type AttachmentUpload struct {
	File       *multipart.FileHeader
	TicketID   int64
	UploaderID int64
}

type AttachmentFile struct {
	ContentType  string
	ExternalURL  string
	OriginalName string
	Path         string
	SizeBytes    int64
	Storage      string
	StoredName   string
}

type Activity struct {
	Action     string         `json:"action"`
	ActorID    string         `json:"actorId"`
	ActorName  string         `json:"actorName"`
	ActorType  string         `json:"actorType"`
	CreatedAt  string         `json:"createdAt"`
	Detail     map[string]any `json:"detail"`
	ID         string         `json:"id"`
	Visibility string         `json:"visibility"`
}

type Statistics struct {
	AverageFirstResponseMinutes float64          `json:"averageFirstResponseMinutes"`
	AverageResolutionMinutes    float64          `json:"averageResolutionMinutes"`
	Created                     int64            `json:"created"`
	Daily                       []StatisticsDay  `json:"daily"`
	FirstResponseOverdue        int64            `json:"firstResponseOverdue"`
	RangeDays                   int              `json:"rangeDays"`
	ResolutionOverdue           int64            `json:"resolutionOverdue"`
	Resolved                    int64            `json:"resolved"`
	Status                      map[string]int64 `json:"status"`
	Total                       int64            `json:"total"`
}

type StatisticsDay struct {
	Created  int64  `json:"created"`
	Date     string `json:"date"`
	Resolved int64  `json:"resolved"`
}

type QuickReply struct {
	Category  string `json:"category"`
	ContentEN string `json:"contentEn"`
	ContentZH string `json:"contentZh"`
	ID        string `json:"id"`
	SortNo    int    `json:"sortNo"`
	Status    int    `json:"status"`
	Title     string `json:"title"`
}

type SaveQuickReplyRequest struct {
	Category  string `json:"category"`
	ContentEN string `json:"contentEn"`
	ContentZH string `json:"contentZh"`
	SortNo    int    `json:"sortNo"`
	Status    *int   `json:"status"`
	Title     string `json:"title"`
}

type Event struct {
	ActorID   string `json:"actorId"`
	ID        string `json:"id"`
	MessageID string `json:"messageId,omitempty"`
	TicketID  string `json:"ticketId"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	UserID    string `json:"userId"`
}
