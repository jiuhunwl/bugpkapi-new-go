package observability

import (
	"context"
	"strconv"
	"strings"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	// defaultSuggestLimit is returned when the caller omits limit.
	defaultSuggestLimit = 10
	// maxSuggestLimit caps remote-search results; dropdowns only need a page.
	maxSuggestLimit = 20
	// suggestMaxKeywordLen protects the prepared statement from overlong input.
	suggestMaxKeywordLen = 64
)

// SuggestItem is the unified remote-search protocol row consumed by the
// frontend ApiSelect remote-search mode. ID is the stable key, Label is the
// primary display text, SubLabel is secondary context (username / key mask).
type SuggestItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	SubLabel    string `json:"subLabel,omitempty"`
	Keyword     string `json:"keyword,omitempty"`
	Description string `json:"description,omitempty"`
}

func clampSuggestLimit(limit int) int {
	if limit <= 0 {
		return defaultSuggestLimit
	}
	if limit > maxSuggestLimit {
		return maxSuggestLimit
	}
	return limit
}

// suggestKeywordTooShort mirrors the fuzzy guard: empty is allowed (means
// "top candidates"), a single ASCII char is rejected, a single CJK char is
// meaningful.
func suggestKeywordTooShort(keyword string) bool {
	trimmed := strings.TrimSpace(keyword)
	if trimmed == "" {
		return false
	}
	runes := []rune(trimmed)
	if len(runes) >= 2 {
		return false
	}
	return runes[0] < 0x4E00 || runes[0] > 0x9FFF
}

func suggestKeywordOverlong(keyword string) bool {
	return len([]byte(strings.TrimSpace(keyword))) > suggestMaxKeywordLen
}

// ---- repository ----

// SuggestUsers returns a bounded keyword search over bp_user. Order is by
// ascending id so results are stable; the InnoDB index on username is used
// for the equality path, and the LIKE scan is bounded by limit.
func (r *Repository) SuggestUsers(ctx context.Context, keyword string, limit int) ([]SuggestItem, error) {
	keyword = strings.TrimSpace(keyword)
	where := ""
	args := []any{}
	if keyword != "" {
		like := "%" + keyword + "%"
		where = " where (u.username like ? or u.display_name like ?)"
		args = append(args, like, like)
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `
		select u.id, coalesce(nullif(u.display_name,''),u.username), u.username
		from bp_user u`+where+`
		order by u.id asc
		limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SuggestItem, 0, limit)
	for rows.Next() {
		var id int64
		var item SuggestItem
		if err := rows.Scan(&id, &item.Label, &item.SubLabel); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.Keyword = item.SubLabel
		items = append(items, item)
	}
	return items, rows.Err()
}

// SuggestInterfaces returns bounded matching bp_interface_api rows that are
// enabled (status=1).
func (r *Repository) SuggestInterfaces(ctx context.Context, keyword string, limit int) ([]SuggestItem, error) {
	keyword = strings.TrimSpace(keyword)
	where := " where i.status=1"
	args := []any{}
	if keyword != "" {
		like := "%" + keyword + "%"
		where += " and (i.api_code like ? or i.name like ?)"
		args = append(args, like, like)
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `
		select i.id, i.api_code, i.name
		from bp_interface_api i`+where+`
		order by i.id asc
		limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SuggestItem, 0, limit)
	for rows.Next() {
		var id int64
		var item SuggestItem
		if err := rows.Scan(&id, &item.Keyword, &item.Label); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}

// SuggestAPIKeys returns bounded matching bp_api_key rows. For non-admins the
// search is scoped to the caller's own keys only.
func (r *Repository) SuggestAPIKeys(ctx context.Context, ownerID int64, admin bool, keyword string, limit int) ([]SuggestItem, error) {
	keyword = strings.TrimSpace(keyword)
	where := []string{"1=1"}
	args := []any{}
	if !admin {
		where = append(where, "k.user_id=?")
		args = append(args, ownerID)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		where = append(where, "(k.name like ? or k.key_prefix like ? or k.key_suffix like ?)")
		args = append(args, like, like, like)
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `
		select k.id, k.name, concat(k.key_prefix,'****',k.key_suffix)
		from bp_api_key k
		where `+strings.Join(where, " and ")+`
		order by k.id asc
		limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SuggestItem, 0, limit)
	for rows.Next() {
		var id int64
		var item SuggestItem
		if err := rows.Scan(&id, &item.Label, &item.SubLabel); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.Keyword = item.Label
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---- service ----

func (s *Service) SuggestUsers(ctx context.Context, principal rbac.Principal, keyword string, limit int) ([]SuggestItem, error) {
	if err := s.authz.Require(ctx, principal, "log:call:list"); err != nil {
		return nil, err
	}
	if err := validateSuggestKeyword(keyword); err != nil {
		return nil, err
	}
	return s.repo.SuggestUsers(ctx, keyword, clampSuggestLimit(limit))
}

func (s *Service) SuggestInterfaces(ctx context.Context, principal rbac.Principal, keyword string, limit int) ([]SuggestItem, error) {
	if err := s.authz.Require(ctx, principal, "log:call:list"); err != nil {
		return nil, err
	}
	if err := validateSuggestKeyword(keyword); err != nil {
		return nil, err
	}
	return s.repo.SuggestInterfaces(ctx, keyword, clampSuggestLimit(limit))
}

func (s *Service) SuggestAPIKeys(ctx context.Context, principal rbac.Principal, keyword string, limit int) ([]SuggestItem, error) {
	if err := s.authz.Require(ctx, principal, "log:call:list"); err != nil {
		return nil, err
	}
	if err := validateSuggestKeyword(keyword); err != nil {
		return nil, err
	}
	return s.repo.SuggestAPIKeys(ctx, principal.ID, hasRole(principal.Roles, "admin"), keyword, clampSuggestLimit(limit))
}

func validateSuggestKeyword(keyword string) error {
	if suggestKeywordTooShort(keyword) {
		return apperrors.New(consts.StatusBadRequest, "keyword needs at least 2 characters (a single CJK character is allowed)")
	}
	if suggestKeywordOverlong(keyword) {
		return apperrors.New(consts.StatusBadRequest, "keyword is too long")
	}
	return nil
}
