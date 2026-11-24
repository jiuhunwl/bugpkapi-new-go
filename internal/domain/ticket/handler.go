package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/imagehost"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/pagination"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/upload"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	attachmentDir    string
	attachmentPolicy upload.AttachmentPolicy
	imageHostConfig  ImageHostConfigSource
	imageHost        *imagehost.Uploader
	service          *Service
}

type ImageHostConfigSource interface {
	ImageHostConfig(context.Context) (imagehost.Config, error)
}

func NewHandler(service *Service, maxRequestBodyBytes int) *Handler {
	return &Handler{
		attachmentDir:    filepath.Join("uploads", "ticket-attachment"),
		attachmentPolicy: upload.AttachmentPolicyForBodyLimit(maxRequestBodyBytes),
		service:          service,
	}
}

func (h *Handler) SetImageHost(source ImageHostConfigSource, uploader *imagehost.Uploader) {
	h.imageHostConfig = source
	h.imageHost = uploader
}

func (h *Handler) Register(group *route.RouterGroup) {
	group.GET("", h.list)
	group.POST("", h.create)
	group.GET("/stats", h.statistics)
	group.GET("/attachment-policy", h.attachmentCapabilities)
	group.GET("/quick-replies", h.quickReplies)
	group.POST("/quick-replies", h.createQuickReply)
	group.PUT("/quick-replies/:quickReplyId", h.updateQuickReply)
	group.DELETE("/quick-replies/:quickReplyId", h.deleteQuickReply)
	group.GET("/stream", h.stream)
	group.GET("/users/:userId", h.userProfile)
	group.GET("/:id", h.detail)
	group.POST("/:id/messages", h.reply)
	group.POST("/:id/notes", h.internalNote)
	group.POST("/:id/close", h.closeTicket)
	group.POST("/:id/attachments", h.uploadAttachment)
	group.GET("/:id/attachments/:attachmentId", h.downloadAttachment)
	group.DELETE("/:id/attachments/:attachmentId", h.deleteAttachment)
	group.PUT("/:id", h.update)
	group.POST("/:id/read", h.markRead)
}

func (h *Handler) statistics(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	days, err := boundedQueryInt(string(c.Query("days")), 7, 1, 30)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Statistics(ctx, principal, days)
	respond(c, result, err)
}

func (h *Handler) quickReplies(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.QuickReplies(ctx, principal)
	respond(c, result, err)
}

func (h *Handler) createQuickReply(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveQuickReplyRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.CreateQuickReply(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) updateQuickReply(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	id, err := namedPathID(c, "quickReplyId")
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveQuickReplyRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.UpdateQuickReply(ctx, principal, id, req)
	respond(c, result, err)
}

func (h *Handler) deleteQuickReply(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	id, err := namedPathID(c, "quickReplyId")
	if err != nil {
		response.FromError(c, err)
		return
	}
	respond(c, true, h.service.DeleteQuickReply(ctx, principal, id))
}

func (h *Handler) list(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	page, err := boundedQueryInt(string(c.Query("page")), 1, 1, pagination.MaxPage)
	if err != nil {
		response.FromError(c, err)
		return
	}
	pageSize, err := boundedQueryInt(string(c.Query("pageSize")), 20, 1, 100)
	if err != nil {
		response.FromError(c, err)
		return
	}
	unread, err := binaryQueryFlag(string(c.Query("unread")))
	if err != nil {
		response.FromError(c, err)
		return
	}
	overdue, err := binaryQueryFlag(string(c.Query("overdue")))
	if err != nil {
		response.FromError(c, err)
		return
	}
	assignedTo, err := boundedQueryInt(string(c.Query("assignedTo")), 0, 0, 1_000_000_000)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.List(ctx, principal, ListParams{
		AssignedTo:  int64(assignedTo),
		Category:    string(c.Query("category")),
		CreatedFrom: string(c.Query("createdFrom")),
		CreatedTo:   string(c.Query("createdTo")),
		Keyword:     string(c.Query("keyword")),
		Overdue:     overdue,
		Page:        page,
		PageSize:    pageSize,
		Priority:    string(c.Query("priority")),
		SortBy:      string(c.Query("sortBy")),
		SortOrder:   string(c.Query("sortOrder")),
		Status:      string(c.Query("status")),
		Unread:      unread,
		UpdatedFrom: string(c.Query("updatedFrom")),
		UpdatedTo:   string(c.Query("updatedTo")),
	})
	respond(c, result, err)
}

func (h *Handler) create(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req CreateRequest
	if !bind(c, &req) {
		return
	}
	req.Language = auth.ResolveEmailLanguage(
		req.Language,
		"",
		string(c.Request.Header.Peek("X-App-Locale")),
		string(c.Request.Header.Peek("Accept-Language")),
		"",
	)
	result, err := h.service.Create(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) userProfile(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	userID, err := namedPathID(c, "userId")
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UserProfile(ctx, principal, userID)
	respond(c, result, err)
}

func (h *Handler) detail(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Detail(ctx, principal, ticketID)
	respond(c, result, err)
}

func (h *Handler) reply(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req ReplyRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.Reply(ctx, principal, ticketID, req)
	respond(c, result, err)
}

func (h *Handler) internalNote(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req InternalNoteRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.AddInternalNote(ctx, principal, ticketID, req)
	respond(c, result, err)
}

func (h *Handler) closeTicket(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Close(ctx, principal, ticketID)
	respond(c, result, err)
}

func (h *Handler) uploadAttachment(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "file is required"))
		return
	}
	if h.imageHostConfig != nil && upload.IsImageFilename(fileHeader.Filename) {
		cfg, configErr := h.imageHostConfig.ImageHostConfig(ctx)
		if configErr != nil {
			response.FromError(c, apperrors.Wrap(consts.StatusServiceUnavailable, "图床配置读取失败", configErr))
			return
		}
		if cfg.Enabled == 1 {
			if err := h.service.AuthorizeAttachmentUpload(ctx, principal, ticketID); err != nil {
				response.FromError(c, err)
				return
			}
			h.uploadExternalImage(ctx, c, principal, ticketID, fileHeader, cfg)
			return
		}
	}
	saved, err := upload.SaveAttachmentWithLimit(fileHeader, h.attachmentDir, h.attachmentPolicy.MaxBytes)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AddAttachment(ctx, principal, ticketID, AttachmentFile{
		ContentType:  saved.ContentType,
		OriginalName: saved.OriginalName,
		SizeBytes:    saved.SizeBytes,
		Storage:      AttachmentStorageLocal,
		StoredName:   saved.StoredName,
	})
	if err != nil {
		_ = upload.RemoveAttachment(h.attachmentDir, saved.StoredName)
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) uploadExternalImage(ctx context.Context, c *hertzapp.RequestContext, principal Principal, ticketID int64, fileHeader *multipart.FileHeader, cfg imagehost.Config) {
	if h.imageHost == nil {
		response.FromError(c, apperrors.New(consts.StatusServiceUnavailable, "图床上传客户端未初始化"))
		return
	}
	maxBytes := cfg.MaxBytes
	if h.attachmentPolicy.MaxBytes > 0 && h.attachmentPolicy.MaxBytes < maxBytes {
		maxBytes = h.attachmentPolicy.MaxBytes
	}
	image, err := upload.ReadValidatedImage(fileHeader, maxBytes)
	if err != nil {
		response.FromError(c, err)
		return
	}
	uploaded, err := h.imageHost.Upload(ctx, cfg, imagehost.File{
		ContentType: image.ContentType,
		Data:        image.Data,
		Filename:    image.OriginalName,
	})
	if err != nil {
		response.FromError(c, apperrors.Wrap(consts.StatusBadGateway, "图片上传到图床失败", err))
		return
	}
	storedName, err := upload.NewStoredName(image.Ext)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AddAttachment(ctx, principal, ticketID, AttachmentFile{
		ContentType:  image.ContentType,
		ExternalURL:  uploaded.URL,
		OriginalName: image.OriginalName,
		SizeBytes:    image.SizeBytes,
		Storage:      uploaded.Provider,
		StoredName:   storedName,
	})
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) downloadAttachment(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	attachmentID, err := namedPathID(c, "attachmentId")
	if err != nil {
		response.FromError(c, err)
		return
	}
	file, err := h.service.Attachment(ctx, principal, ticketID, attachmentID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if externalURL := strings.TrimSpace(file.ExternalURL); externalURL != "" {
		c.Response.Header.Set("Location", externalURL)
		c.SetStatusCode(consts.StatusFound)
		return
	}
	path, err := upload.ResolveAttachmentPath(h.attachmentDir, file.StoredName)
	if err != nil {
		response.FromError(c, err)
		return
	}
	encodedName := strings.ReplaceAll(url.PathEscape(file.OriginalName), "+", "%20")
	c.Response.Header.Set("Content-Disposition", "attachment; filename=attachment; filename*=UTF-8''"+encodedName)
	c.Response.Header.Set("Cache-Control", "private, no-store")
	if strings.TrimSpace(file.ContentType) != "" {
		c.Response.Header.SetContentType(file.ContentType)
	}
	c.File(path)
}

func (h *Handler) deleteAttachment(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	attachmentID, err := namedPathID(c, "attachmentId")
	if err != nil {
		response.FromError(c, err)
		return
	}
	storedName, err := h.service.DeleteAttachment(ctx, principal, ticketID, attachmentID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	_ = upload.RemoveAttachment(h.attachmentDir, storedName)
	response.OK(c, true)
}

func (h *Handler) update(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req UpdateRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.Update(ctx, principal, ticketID, req)
	respond(c, result, err)
}

func (h *Handler) markRead(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	ticketID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req MarkReadRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.MarkRead(ctx, principal, ticketID, req)
	respond(c, result, err)
}

func (h *Handler) stream(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var ticketID int64
	if raw := strings.TrimSpace(string(c.Query("ticketId"))); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "ticketId不合法"))
			return
		}
		ticketID = value
	}
	subscription, err := h.service.Subscribe(ctx, principal, ticketID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	reader, writer := io.Pipe()
	c.Response.Header.SetContentType("text/event-stream; charset=utf-8")
	c.Response.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Response.Header.Set("Connection", "keep-alive")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	c.SetStatusCode(consts.StatusOK)
	c.SetBodyStream(reader, -1)
	go writeTicketEventStream(writer, subscription, h.service, principal.ID)
}

// ticketBanProbeInterval bounds how long a banned account can keep receiving
// live ticket events on a connection that was opened before the ban landed.
const ticketBanProbeInterval = 60 * time.Second

func writeTicketEventStream(writer *io.PipeWriter, subscription *Subscription, service *Service, userID int64) {
	defer func() {
		_ = subscription.Close()
		_ = writer.Close()
	}()
	if _, err := io.WriteString(writer, "retry: 3000\nevent: ready\ndata: {}\n\n"); err != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	banProbe := time.NewTicker(ticketBanProbeInterval)
	defer banProbe.Stop()
	for {
		select {
		case event, ok := <-subscription.Events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(writer, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return
			}
		case <-banProbe.C:
			if !streamAccountActive(service, userID) {
				// Announce the eviction, then close. The browser reconnects with
				// its credential, the guard rejects the banned account, and the
				// client enters its ban flow on its own.
				_, _ = io.WriteString(writer, "event: account-banned\ndata: {}\n\n")
				return
			}
		}
	}
}

// streamAccountActive fails open on a read error: a transient database problem
// must not terminate a healthy stream. The next probe retries, and a ban always
// wins on the following reconnect because that path authenticates from scratch.
func streamAccountActive(service *Service, userID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active, err := service.AccountActive(ctx, userID)
	if err != nil {
		return true
	}
	return active
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	principal, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return principal, true
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bind(c *hertzapp.RequestContext, target any) bool {
	if err := jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return false
	}
	return true
}

func bearer(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func pathID(c *hertzapp.RequestContext) (int64, error) {
	return namedPathID(c, "id")
}

func namedPathID(c *hertzapp.RequestContext, name string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param(name)), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "工单ID不合法")
	}
	return id, nil
}

func boundedQueryInt(raw string, fallback, min, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, apperrors.New(consts.StatusBadRequest, "分页参数不合法")
	}
	return value, nil
}

func binaryQueryFlag(raw string) (bool, error) {
	switch strings.TrimSpace(raw) {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, apperrors.New(consts.StatusBadRequest, "筛选参数不合法")
	}
}

func (h *Handler) attachmentCapabilities(ctx context.Context, c *hertzapp.RequestContext) {
	if _, ok := h.principal(ctx, c); !ok {
		return
	}
	response.OK(c, h.attachmentPolicy)
}
