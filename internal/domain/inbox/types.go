package inbox

// Item 是一条站内消息的用户视角视图。
type Item struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Kind      string `json:"kind"`
	IsRead    bool   `json:"isRead"`
	ReadAt    string `json:"readAt"`
	CreatedAt string `json:"createdAt"`
}

type Page struct {
	Items    []Item `json:"items"`
	Total    int64  `json:"total"`
	Unread   int64  `json:"unread"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

// BroadcastRequest 是管理端发送全站/定向通知的请求。
type BroadcastRequest struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	SendEmail bool   `json:"sendEmail"`
	UserID    int64  `json:"userId"`
}

type BroadcastResult struct {
	Recipients int64 `json:"recipients"`
	Emails     int64 `json:"emails"`
}
