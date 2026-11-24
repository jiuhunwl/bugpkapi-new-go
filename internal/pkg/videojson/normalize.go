// Package videojson converts the loosely shaped JSON returned by short-video
// upstreams into the project's unified video-parser response contract.
package videojson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/orderedjson"
)

const (
	successCode       = 200
	defaultSuccessMsg = "解析成功"
	defaultErrorMsg   = "解析失败"
)

// Result is the result of a response normalization attempt.
//
// IsJSON is false when the input is clearly not JSON. Such responses are
// intentionally passed through unchanged so enabling the cleaner on a mixed
// response interface does not corrupt text, XML, HTML, or binary responses.
type Result struct {
	Body    []byte
	Changed bool
	IsJSON  bool
}

// Normalize converts a JSON response to the unified envelope. Non-JSON input
// is returned byte-for-byte (apart from an optional UTF-8 BOM removal only for
// JSON detection) and does not produce an error.
func Normalize(data []byte) ([]byte, error) {
	result, err := NormalizeWithResult(data)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}

// Clean is an alias for Normalize, useful at call sites that describe the
// operation as response cleaning.
func Clean(data []byte) ([]byte, error) {
	return Normalize(data)
}

// NormalizeWithResult is the detailed form of Normalize used by the gateway.
func NormalizeWithResult(data []byte) (Result, error) {
	original := append([]byte(nil), data...)
	trimmed := bytes.TrimSpace(data)
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xef, 0xbb, 0xbf})
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return Result{Body: original}, nil
	}

	value, err := jsonrequest.DecodeUnambiguous(trimmed, jsonrequest.DefaultMaxDepth)
	if err != nil {
		// A body that looks like JSON but is malformed is a real cleaning
		// failure. The caller can then try the next upstream node instead of
		// silently returning a response that violates the enabled contract.
		return Result{}, fmt.Errorf("%w: decode upstream JSON: %w", ErrMalformedJSON, err)
	}
	if value == nil {
		return Result{Body: original, IsJSON: true}, nil
	}
	if _, ok := value.(map[string]any); !ok {
		if _, ok := value.([]any); !ok {
			// JSON scalars are not parser payloads. Preserve them just like a
			// non-JSON response; this keeps the optional feature non-invasive.
			return Result{Body: original, IsJSON: true}, nil
		}
	}

	envelope, _, _ := normalizeEnvelope(value)

	return Result{
		Body:    envelope,
		Changed: !bytes.Equal(original, envelope),
		IsJSON:  true,
	}, nil
}

func normalizeEnvelope(root any) ([]byte, int, string) {
	responseSources := envelopeSources(root)
	code, foundCode := responseCodeFromSources(root, responseSources)
	if !foundCode {
		code = successCode
	}
	message := responseMessageFromSources(responseSources)
	if message == "" {
		if code == successCode {
			message = defaultSuccessMsg
		} else {
			message = defaultErrorMsg
		}
	}

	payload, sources := locatePayload(root)

	env := orderedjson.NewObject()
	env.Set("code", code)
	env.Set("msg", message)

	if code != successCode {
		// Error payloads are deliberately not interpreted as media.
		data, ok := firstSourceValue(sources, "data", "result", "payload", "response", "body")
		if !ok || data == nil {
			env.Set("data", []any{})
		} else {
			env.Set("data", data)
		}
		return env.Bytes(), code, message
	}

	mediaData := buildOrderedMediaData(payload, sources)
	env.Set("data", json.RawMessage(mediaData))
	return env.Bytes(), successCode, message
}

func responseCode(root any) (int, bool) {
	return responseCodeFromSources(root, envelopeSources(root))
}

func responseMessage(root any) string {
	return responseMessageFromSources(envelopeSources(root))
}

// envelopeSources walks the conventional response wrappers without requiring
// the wrapped value to already look like a media object. This is intentionally
// separate from locatePayload: status/message fields are often kept beside an
// inner data/result object, including on error responses.
func envelopeSources(root any) []map[string]any {
	current := root
	var sources []map[string]any
	for depth := 0; depth < 8; depth++ {
		m, ok := current.(map[string]any)
		if !ok {
			break
		}
		sources = append(sources, m)
		var next any
		for _, name := range []string{"data", "result", "payload", "response", "body", "content"} {
			candidate, exists := lookup(m, name)
			if !exists || candidate == nil {
				continue
			}
			if _, mapOK := candidate.(map[string]any); mapOK {
				next = candidate
				break
			}
			if _, arrayOK := candidate.([]any); arrayOK {
				// Arrays cannot contain envelope-level status fields in a
				// reliable way, so retain the current source and stop here.
				break
			}
		}
		if next == nil || sameValue(next, current) {
			break
		}
		current = next
	}
	return sources
}

func responseCodeFromSources(root any, sources []map[string]any) (int, bool) {
	if len(sources) == 0 {
		if m, ok := root.(map[string]any); ok {
			sources = []map[string]any{m}
		} else {
			return successCode, false
		}
	}

	type candidate struct {
		value any
	}
	var candidates []candidate
	failed := false
	succeeded := false
	for _, source := range sources {
		if value, _, ok := lookupEntry(source, "code", "status_code", "statusCode", "status", "errno", "errcode", "error_code", "errorCode"); ok && value != nil {
			candidates = append(candidates, candidate{value: value})
		}
		if value, _, ok := lookupEntry(source, "success", "ok"); ok {
			if flag, valid := boolValue(value); valid {
				if flag {
					succeeded = true
				} else {
					failed = true
				}
			}
		}
	}

	// An explicit failure flag is stronger than code:0, which is frequently
	// emitted by wrappers even when the nested request failed.
	if failed {
		for _, item := range candidates {
			if number, ok := intValue(item.value); ok && number >= 400 {
				return number, true
			}
		}
		return 500, true
	}

	// Prefer a nested, explicit error code over an outer success wrapper.
	for _, item := range candidates {
		if number, ok := intValue(item.value); ok && !responseCodeNumberIsSuccess(number) {
			return number, true
		}
		if text := strings.ToLower(strings.TrimSpace(stringValue(item.value))); text != "" {
			switch text {
			case "false", "error", "err", "fail", "failed", "failure":
				return 500, true
			}
		}
	}
	if len(candidates) > 0 {
		item := candidates[0]
		if number, ok := intValue(item.value); ok {
			if responseCodeNumberIsSuccess(number) {
				return successCode, true
			}
			return number, true
		}
		if text := strings.ToLower(strings.TrimSpace(stringValue(item.value))); text != "" {
			switch text {
			case "ok", "success", "succeeded", "true":
				return successCode, true
			}
		}
	}
	if succeeded {
		return successCode, true
	}
	return successCode, false
}

func responseCodeNumberIsSuccess(number int) bool {
	if number == 0 || (number >= 200 && number < 300) {
		return true
	}
	// `status: 1` (and the equivalent status_code/errno variants) is a
	// widespread success convention. Treat code:1 as success as well; an
	// explicit success:false flag was handled above.
	if number == 1 {
		return true
	}
	return false
}

func responseMessageFromSources(sources []map[string]any) string {
	for _, source := range sources {
		value, _, found := lookupEntry(source, "msg", "message", "errmsg", "error_message", "errorMessage", "error")
		if found {
			if message := stringValue(value); message != "" {
				return message
			}
		}
	}
	return ""
}

// locatePayload returns the deepest wrapper that looks like a media payload,
// together with all wrapper maps (outer first). The full source chain lets the
// normalizer retain metadata that some upstreams put beside data/result.
func locatePayload(root any) (any, []map[string]any) {
	current := root
	var sources []map[string]any
	for depth := 0; depth < 6; depth++ {
		m, ok := current.(map[string]any)
		if !ok {
			break
		}
		sources = append(sources, m)
		var next any
		var fallback any
		for _, name := range []string{"data", "result", "payload", "response", "body", "content"} {
			candidate, exists := lookup(m, name)
			if !exists || candidate == nil {
				continue
			}
			if fallback == nil {
				fallback = candidate
			}
			if mediaValue(candidate) {
				next = candidate
				break
			}
		}
		if next == nil {
			next = fallback
		}
		if next == nil || sameValue(next, current) {
			break
		}
		if _, mapOK := next.(map[string]any); !mapOK {
			if _, arrayOK := next.([]any); !arrayOK {
				break
			}
		}
		current = next
	}
	if len(sources) == 0 {
		return root, nil
	}
	return current, sources
}

func mediaValue(value any) bool {
	if _, ok := value.([]any); ok {
		return true
	}
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, name := range []string{
		"type", "resource_type", "resourceType", "url", "video", "video_url", "videoUrl",
		"play_url", "playUrl", "download_url", "downloadUrl", "no_watermark_url", "noWatermarkUrl",
		"original_url", "originalUrl", "origin_url", "originUrl", "images", "image_urls",
		"imageUrls", "pics", "pictures", "photos", "live_photo", "livePhoto", "live_photos",
		"title", "desc", "description", "caption", "author", "user", "creator", "cover",
	} {
		if _, ok := lookup(m, name); ok {
			return true
		}
	}
	return false
}

// buildOrderedMediaData produces the ordered data.{type,title,desc,author,…} JSON.
// Unlike normalizeMediaData which returns map[string]any (alphabetized on marshal),
// this function builds ordered JSON bytes so field order matches the documented
// contract and is consistent with the upstream source structure.
func buildOrderedMediaData(payload any, sources []map[string]any) []byte {
	if len(sources) == 0 {
		if m, ok := payload.(map[string]any); ok {
			sources = []map[string]any{m}
		}
	}
	if items, ok := payload.([]any); ok {
		for _, item := range items {
			if m, mapOK := item.(map[string]any); mapOK {
				sources = append(sources, m)
			} else if text := stringValue(item); text != "" {
				sources = append(sources, map[string]any{"image": text})
			}
		}
	}

	explicitType := strings.ToLower(strings.TrimSpace(firstString(sources, "type", "resource_type", "resourceType", "media_type", "mediaType")))
	videoURL := firstURLAliasPriority(sources, videoURLAliases...)
	genericURLs := genericURLsFromPayload(payload)
	images := collectImages(sources)
	livePhotos := collectLivePhotos(sources)
	backups := collectVideoBackups(sources)

	if explicitType == "image" || explicitType == "images" || explicitType == "photo" || explicitType == "gallery" || explicitType == "picture" || strings.HasPrefix(explicitType, "live") {
		images = append(images, genericURLs...)
	} else if videoURL == "" && len(images) == 0 && len(livePhotos) == 0 {
		if len(genericURLs) > 1 && explicitType == "" {
			images = append(images, genericURLs...)
		} else if len(genericURLs) > 0 {
			videoURL = genericURLs[0]
		}
	}

	resourceType := resourceTypeFor(explicitType, videoURL, images, livePhotos)
	if resourceType == "video" && videoURL == "" && len(backups) > 0 {
		videoURL = stringValue(backups[0]["url"])
	}
	if resourceType != "video" {
		videoURL = ""
	}
	if (resourceType == "image" || resourceType == "live") && len(livePhotos) > 0 {
		for _, item := range livePhotos {
			if image := stringValue(item["image"]); image != "" {
				images = append(images, image)
			}
		}
	}
	images = uniqueStrings(images)
	if resourceType == "video" {
		images = []string{}
		livePhotos = []map[string]any{}
	}
	if resourceType == "image" {
		livePhotos = []map[string]any{}
	}

	cover := firstURL(sources, "cover", "cover_url", "coverUrl", "thumbnail", "thumbnail_url", "thumbnailUrl", "poster", "poster_url", "posterUrl", "thumb")
	if cover == "" && len(images) > 0 {
		cover = images[0]
	}

	title := firstString(sources, "title", "desc", "description", "caption", "text", "name")
	desc := firstString(sources, "desc", "description", "caption", "text", "title")
	author := normalizeAuthor(firstValue(sources, "author", "user", "creator", "owner", "user_info", "userInfo"), sources)
	quality := firstString(sources, "quality", "video_quality", "videoQuality", "resolution", "definition")
	duration := normalizeDuration(firstValue(sources, "duration", "duration_seconds", "durationSeconds"))
	if duration == nil {
		if milliseconds, ok := firstSourceValue(sources, "duration_ms", "durationMs"); ok {
			duration = normalizeMilliseconds(milliseconds)
		}
	}
	music := normalizeMusic(firstValue(sources, "music", "audio", "song", "sound"))
	extra := collectExtra(sources)

	// Build data object with documented field order.
	obj := orderedjson.NewObject()
	obj.Set("type", resourceType)
	obj.Set("title", title)
	obj.Set("desc", desc)
	obj.Set("author", author)
	obj.Set("cover", cover)
	if resourceType == "video" {
		obj.Set("url", videoURL)
	} else {
		obj.Set("url", nil)
	}
	obj.Set("quality", quality)
	obj.Set("duration", duration)
	obj.Set("images", images)
	obj.Set("live_photo", livePhotos)
	obj.Set("music", music)
	obj.Set("extra", extra)
	if resourceType == "video" && len(backups) > 0 {
		obj.Set("video_backup", backups)
	}

	return obj.Bytes()
}

func resourceTypeFor(explicit, videoURL string, images []string, livePhotos []map[string]any) string {
	switch explicit {
	case "live", "live_photo", "live-photo", "livephoto", "livephoto_gallery":
		return "live"
	case "image", "images", "photo", "gallery", "picture":
		return "image"
	case "video", "movie", "short_video", "short-video":
		return "video"
	}
	if len(livePhotos) > 0 {
		return "live"
	}
	if videoURL != "" {
		return "video"
	}
	if len(images) > 0 {
		return "image"
	}
	return "video"
}

var videoURLAliases = []string{
	// Prefer no-watermark/original resources before ordinary playback URLs.
	"no_watermark_url", "noWatermarkUrl", "nowatermark_url", "nowatermarkUrl",
	"without_watermark_url", "withoutWatermarkUrl", "clean_url", "cleanUrl",
	"original_url", "originalUrl", "origin_url", "originUrl", "origin",
	"high_quality_url", "highQualityUrl", "hd_url", "hdUrl",
	"download_url", "downloadUrl", "video_url", "videoUrl", "play_url", "playUrl",
	"playback_url", "playbackUrl", "video_src", "videoSrc", "video", "src", "source",
}

var imageURLAliases = []string{
	"images", "image_urls", "imageUrls", "pics", "pictures", "photos", "image_list", "imageList",
	"picture_urls", "pictureUrls", "photo_urls", "photoUrls", "image_url", "imageUrl", "image", "pic", "picture",
}

var backupAliases = []string{
	"video_backup", "videoBackup", "videos", "video_list", "videoList", "video_urls", "videoUrls",
	"play_urls", "playUrls", "qualities", "quality_list", "qualityList",
}

func genericURLsFromPayload(payload any) []string {
	var result []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				result = append(result, text)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if url := firstURL([]map[string]any{typed}, "url", "uri", "src"); url != "" {
				result = append(result, url)
			}
		}
	}
	walk(payload)
	return uniqueStrings(result)
}

func collectImages(sources []map[string]any) []string {
	var result []string
	for _, source := range sources {
		for _, alias := range imageURLAliases {
			value, ok := lookup(source, alias)
			if !ok {
				continue
			}
			result = append(result, imageStrings(value)...)
		}
	}
	return uniqueStrings(result)
}

func imageStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			return []string{value}
		}
	case []any:
		var result []string
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				if value := firstURL([]map[string]any{m}, "no_watermark_url", "noWatermarkUrl", "original_url", "originalUrl", "origin_url", "originUrl", "url", "image", "image_url", "imageUrl", "src"); value != "" {
					result = append(result, value)
				}
				continue
			}
			result = append(result, imageStrings(item)...)
		}
		return result
	case map[string]any:
		if value := firstURL([]map[string]any{typed}, "no_watermark_url", "noWatermarkUrl", "original_url", "originalUrl", "origin_url", "originUrl", "url", "image", "image_url", "imageUrl", "src"); value != "" {
			return []string{value}
		}
	}
	return nil
}

func collectLivePhotos(sources []map[string]any) []map[string]any {
	var result []map[string]any
	for _, source := range sources {
		for _, alias := range []string{"live_photo", "livePhoto", "live_photos", "livePhotos", "live_photo_list", "livePhotoList"} {
			value, ok := lookup(source, alias)
			if !ok {
				continue
			}
			result = append(result, livePhotoItems(value)...)
		}
	}
	return dedupeLivePhotos(result)
}

func livePhotoItems(value any) []map[string]any {
	var result []map[string]any
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			result = append(result, livePhotoItems(item)...)
		}
	case map[string]any:
		image := firstURL([]map[string]any{typed}, "image", "image_url", "imageUrl", "cover", "cover_url", "coverUrl", "url")
		video := firstURL([]map[string]any{typed}, "video", "video_url", "videoUrl", "play_url", "playUrl", "video_src", "videoSrc")
		if image != "" || video != "" {
			result = append(result, map[string]any{"image": image, "video": video})
		}
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			result = append(result, map[string]any{"image": text, "video": ""})
		}
	}
	return result
}

func dedupeLivePhotos(items []map[string]any) []map[string]any {
	seen := map[string]bool{}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		image := stringValue(item["image"])
		video := stringValue(item["video"])
		key := image + "\x00" + video
		if key == "\x00" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}

func collectVideoBackups(sources []map[string]any) []map[string]any {
	var result []map[string]any
	seen := map[string]bool{}
	for _, source := range sources {
		for _, alias := range backupAliases {
			value, ok := lookup(source, alias)
			if !ok {
				continue
			}
			for _, item := range backupItems(value) {
				url := stringValue(item["url"])
				if url == "" || seen[url] {
					continue
				}
				seen[url] = true
				result = append(result, item)
			}
		}
	}
	return result
}

func backupItems(value any) []map[string]any {
	var result []map[string]any
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			result = append(result, backupItems(item)...)
		}
	case map[string]any:
		if nested, ok := lookup(typed, "no_watermark_url", "noWatermarkUrl", "original_url", "originalUrl", "origin_url", "originUrl", "url", "video_url", "videoUrl", "play_url", "playUrl", "download_url", "downloadUrl"); ok {
			url := stringValue(nested)
			if url != "" {
				copy := map[string]any{}
				for key, item := range typed {
					copy[key] = item
				}
				copy["url"] = url
				result = append(result, copy)
			}
			return result
		}
		for _, key := range []string{"qualities", "items", "list", "data"} {
			if nested, ok := lookup(typed, key); ok {
				result = append(result, backupItems(nested)...)
			}
		}
	case string:
		if url := strings.TrimSpace(typed); url != "" {
			result = append(result, map[string]any{"url": url})
		}
	}
	return result
}

func normalizeAuthor(value any, sources []map[string]any) map[string]any {
	result := map[string]any{"name": "", "id": "", "avatar": ""}
	if m, ok := value.(map[string]any); ok {
		result["name"] = firstString([]map[string]any{m}, "name", "nickname", "nick_name", "username", "user_name", "display_name")
		result["id"] = firstString([]map[string]any{m}, "id", "uid", "user_id", "userId", "author_id", "authorId")
		result["avatar"] = firstURL([]map[string]any{m}, "avatar", "avatar_url", "avatarUrl", "head", "head_url", "headUrl", "profile_url", "profileUrl")
	} else if text := stringValue(value); text != "" {
		result["name"] = text
	}
	if result["name"] == "" {
		result["name"] = firstString(sources, "author_name", "authorName", "nickname", "nick_name", "username")
	}
	if result["id"] == "" {
		result["id"] = firstString(sources, "author_id", "authorId", "uid", "user_id", "userId")
	}
	if result["avatar"] == "" {
		result["avatar"] = firstURL(sources, "author_avatar", "authorAvatar", "avatar", "avatar_url", "avatarUrl")
	}
	return result
}

func normalizeMusic(value any) map[string]any {
	result := map[string]any{}
	if value == nil {
		return result
	}
	if text := stringValue(value); text != "" {
		result["url"] = text
		return result
	}
	m, ok := value.(map[string]any)
	if !ok {
		return result
	}
	if title := firstString([]map[string]any{m}, "title", "name", "song", "song_name", "songName"); title != "" {
		result["title"] = title
	}
	if author := firstString([]map[string]any{m}, "author", "artist", "singer", "artist_name", "artistName"); author != "" {
		result["author"] = author
	}
	if url := firstURL([]map[string]any{m}, "url", "music_url", "musicUrl", "audio_url", "audioUrl", "play_url", "playUrl"); url != "" {
		result["url"] = url
	}
	if cover := firstURL([]map[string]any{m}, "cover", "cover_url", "coverUrl", "avatar", "avatar_url", "avatarUrl"); cover != "" {
		result["cover"] = cover
	}
	return result
}

func collectExtra(sources []map[string]any) map[string]any {
	result := map[string]any{}
	for _, source := range sources {
		if value, ok := lookup(source, "extra", "metadata", "meta"); ok {
			if extra, ok := value.(map[string]any); ok {
				for key, item := range extra {
					result[key] = item
				}
			}
		}
		for key, value := range source {
			if recognizedKey(key) {
				continue
			}
			result[key] = value
		}
	}
	return result
}

func recognizedKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, alias := range recognizedAliases {
		if key == strings.ToLower(alias) {
			return true
		}
	}
	return false
}

var recognizedAliases = append([]string{
	"code", "status", "status_code", "statusCode", "errno", "errcode", "error_code", "errorCode", "msg", "message", "errmsg", "error", "error_message", "errorMessage", "success", "ok",
	"data", "result", "payload", "response", "body", "content", "extra", "metadata", "meta",
	"type", "resource_type", "resourceType", "media_type", "mediaType", "title", "desc", "description",
	"caption", "text", "name", "author", "user", "creator", "owner", "user_info", "userInfo",
	"author_name", "authorName", "nickname", "nick_name", "username", "author_id", "authorId", "uid",
	"user_id", "userId", "author_avatar", "authorAvatar", "avatar", "avatar_url", "avatarUrl", "head",
	"head_url", "headUrl", "profile_url", "profileUrl", "cover", "cover_url", "coverUrl", "thumbnail",
	"thumbnail_url", "thumbnailUrl", "poster", "poster_url", "posterUrl", "thumb", "quality", "video_quality",
	"videoQuality", "resolution", "definition", "duration", "duration_seconds", "durationSeconds", "music", "audio", "song", "sound", "images", "image_urls", "imageUrls", "pics", "pictures",
	"photos", "image_list", "imageList", "picture_urls", "pictureUrls", "photo_urls", "photoUrls", "image_url", "imageUrl", "image",
	"pic", "picture", "live_photo", "livePhoto", "live_photos", "livePhotos", "live_photo_list", "livePhotoList",
	"video_backup", "videoBackup", "videos", "video_list", "videoList", "video_urls", "videoUrls", "play_urls",
	"playUrls", "qualities", "quality_list", "qualityList", "url", "uri", "video_src", "videoSrc",
}, videoURLAliases...)

func firstSourceValue(sources []map[string]any, aliases ...string) (any, bool) {
	for index := len(sources) - 1; index >= 0; index-- {
		if value, ok := lookup(sources[index], aliases...); ok {
			return value, true
		}
	}
	return nil, false
}

func firstValue(sources []map[string]any, aliases ...string) any {
	value, _ := firstSourceValue(sources, aliases...)
	return value
}

func firstString(sources []map[string]any, aliases ...string) string {
	for index := len(sources) - 1; index >= 0; index-- {
		if value, ok := lookup(sources[index], aliases...); ok {
			if text := stringValue(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstURL(sources []map[string]any, aliases ...string) string {
	for index := len(sources) - 1; index >= 0; index-- {
		for _, alias := range aliases {
			if value, ok := lookup(sources[index], alias); ok {
				if text := urlValue(value); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

// firstURLAliasPriority is used for the main video resource where a
// no-watermark/original alias must win even when it is supplied by an outer
// envelope and an ordinary playback URL is nested inside data.
func firstURLAliasPriority(sources []map[string]any, aliases ...string) string {
	for _, alias := range aliases {
		for index := len(sources) - 1; index >= 0; index-- {
			if value, ok := lookup(sources[index], alias); ok {
				if text := urlValue(value); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func urlValue(value any) string {
	if text := stringValue(value); text != "" {
		return text
	}
	if m, ok := value.(map[string]any); ok {
		return firstURLAliasPriority([]map[string]any{m}, "no_watermark_url", "noWatermarkUrl", "nowatermark_url", "nowatermarkUrl", "original_url", "originalUrl", "origin_url", "originUrl", "url", "uri", "src", "download_url", "downloadUrl", "play_url", "playUrl", "video_url", "videoUrl", "image_url", "imageUrl")
	}
	return ""
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "ok", "success":
			return true, true
		case "0", "false", "no", "error", "fail":
			return false, true
		}
	case json.Number:
		if number, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return number != 0, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case int64:
		return typed != 0, true
	}
	return false, false
}

func intValue(value any) (int, bool) {
	text := stringValue(value)
	if text == "" {
		return 0, false
	}
	if number, err := strconv.ParseInt(text, 10, 32); err == nil {
		return int(number), true
	}
	return 0, false
}

func normalizeDuration(value any) any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		if seconds, ok := lookup(m, "seconds", "second", "value"); ok {
			return normalizeDuration(seconds)
		}
	}
	if text := stringValue(value); strings.Contains(text, ":") {
		parts := strings.Split(text, ":")
		if len(parts) <= 3 {
			seconds := 0
			for _, part := range parts {
				value, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					return nil
				}
				seconds = seconds*60 + value
			}
			return seconds
		}
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	// Values larger than ten days are overwhelmingly likely to be milliseconds
	// when an upstream uses a generic duration field.
	if number > 86400*10 {
		number /= 1000
	}
	if number < 0 {
		return nil
	}
	return int64(math.Round(number))
}

func normalizeMilliseconds(value any) any {
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil
	}
	return int64(math.Round(number / 1000))
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(typed.String(), 64)
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func lookup(m map[string]any, aliases ...string) (any, bool) {
	value, _, ok := lookupEntry(m, aliases...)
	return value, ok
}

func lookupEntry(m map[string]any, aliases ...string) (any, string, bool) {
	if len(m) == 0 {
		return nil, "", false
	}
	for _, alias := range aliases {
		if value, ok := m[alias]; ok {
			return value, alias, true
		}
	}
	for key, value := range m {
		for _, alias := range aliases {
			if strings.EqualFold(key, alias) {
				return value, key, true
			}
		}
	}
	return nil, "", false
}

func sameValue(left, right any) bool {
	// Wrapper values are maps/slices and cannot be compared directly. Pointer
	// identity is unnecessary here; a self-reference cannot be produced by a
	// JSON decoder, so this helper only guards scalar wrappers.
	switch l := left.(type) {
	case string:
		r, ok := right.(string)
		return ok && l == r
	case json.Number:
		r, ok := right.(json.Number)
		return ok && l == r
	default:
		return false
	}
}

// Keep errors.Is useful to callers that want to classify malformed-looking
// JSON while retaining the human-readable decode context.
var ErrMalformedJSON = errors.New("malformed upstream JSON")
