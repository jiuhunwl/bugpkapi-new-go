package interfaceapi

import (
	"bugpk-api/internal/pkg/jsoncleaning"
)

// FieldMappingPresetGroup 表示一组预设字段映射，用于接口管理中的字段映射配置。
// 每个分组对应短视频统一 JSON 格式中的一个子结构，帮助管理员快速选择常用映射。
type FieldMappingPresetGroup struct {
	// Key 是分组标识（如 "video_resource"），用于前端按分组展示。
	Key string `json:"key"`
	// Label 是分组的中文描述，用于前端展示。
	Label string `json:"label"`
	// Description 是该分组预设映射的用途说明。
	Description string `json:"description"`
	// Presets 是该分组下的预设映射列表。
	Presets []FieldMappingPreset `json:"presets"`
}

// FieldMappingPreset 表示一条预设字段映射建议。
// 管理员可以从预设中选择 source→target 组合，直接添加到接口的字段映射配置中。
type FieldMappingPreset struct {
	// Source 是上游原始 JSON 中的字段名或 JSON 路径（如 "video_url"、"data.author.name"）。
	Source string `json:"source"`
	// SourceLabel 是 source 字段的中文描述（如 "视频地址"）。
	SourceLabel string `json:"sourceLabel"`
	// Target 是统一输出 JSON 中的目标字段名或 JSON 路径（如 "url"、"data.author.name"）。
	Target string `json:"target"`
	// TargetLabel 是 target 字段的中文描述（如 "统一·视频地址"）。
	TargetLabel string `json:"targetLabel"`
	// IsPath 表示该映射是否使用 JSON 路径语法（含点号或 [] 选择器）。
	IsPath bool `json:"isPath"`
	// Note 是使用提示（如 "去水印地址优先级最高"）。
	Note string `json:"note,omitempty"`
}

// FieldMappingPresetsResponse 是字段映射预设接口的响应结构。
type FieldMappingPresetsResponse struct {
	// Groups 是分组后的预设映射列表。
	Groups []FieldMappingPresetGroup `json:"groups"`
	// Reference 是短视频统一输出 JSON 格式参考。
	Reference map[string]string `json:"reference"`
}

// FieldMappingPresets 返回所有短视频统一 JSON 格式的字段映射预设。
// 这些预设基于 videojson 包的输出格式，帮助管理员将各种上游格式
// 映射到统一的短视频解析 JSON 响应格式。
func FieldMappingPresets() FieldMappingPresetsResponse {
	return FieldMappingPresetsResponse{
		Reference: unifiedFormatReference(),
		Groups:    buildPresetGroups(),
	}
}

// unifiedFormatReference 返回短视频统一输出 JSON 格式的字段说明参考。
func unifiedFormatReference() map[string]string {
	return map[string]string{
		"data.type":          "媒体类型：video（视频）、image（图片）、live（实况/Live Photo）",
		"data.title":         "标题/文案",
		"data.desc":          "描述/简介",
		"data.author.name":   "作者名称/昵称",
		"data.author.id":     "作者ID/UID",
		"data.author.avatar": "作者头像URL",
		"data.cover":         "封面图URL",
		"data.url":           "视频/媒体资源URL（去水印优先）",
		"data.quality":       "视频画质/分辨率（如 720p、1080p）",
		"data.duration":      "视频时长（秒，整数）",
		"data.images":        "图片URL列表（gallery/图集模式）",
		"data.live_photo":    "Live Photo 列表 [{image, video}]",
		"data.music.title":   "音乐/歌曲标题",
		"data.music.author":  "音乐/歌曲作者",
		"data.music.url":     "音乐/歌曲地址",
		"data.music.cover":   "音乐/歌曲封面",
		"data.extra":         "额外字段（未被归类的上游数据）",
		"data.video_backup":  "视频备选地址列表（多清晰度/多线路）",
	}
}

// buildPresetGroups 构建所有预设字段映射分组。
func buildPresetGroups() []FieldMappingPresetGroup {
	return []FieldMappingPresetGroup{
		buildVideoResourcePresets(),
		buildAuthorPresets(),
		buildMediaMetadataPresets(),
		buildMusicPresets(),
		buildCoverImagePresets(),
		buildStatusEnvelopePresets(),
	}
}

// buildVideoResourcePresets 构建视频资源字段映射预设。
func buildVideoResourcePresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "video_resource",
		Label:       "视频资源字段",
		Description: "上游视频/资源 URL 字段映射到统一格式的 data.url",
		Presets: []FieldMappingPreset{
			{
				Source: "no_watermark_url", SourceLabel: "去水印链接",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true, Note: "去水印地址优先级最高",
			},
			{
				Source: "noWatermarkUrl", SourceLabel: "去水印链接（驼峰）",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true, Note: "去水印地址优先级最高",
			},
			{
				Source: "original_url", SourceLabel: "原始链接",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true, Note: "无去水印时次选原始资源",
			},
			{
				Source: "video_url", SourceLabel: "视频地址",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
			{
				Source: "videoUrl", SourceLabel: "视频地址（驼峰）",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
			{
				Source: "download_url", SourceLabel: "下载链接",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
			{
				Source: "play_url", SourceLabel: "播放链接",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
			{
				Source: "video_src", SourceLabel: "视频源地址",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
			{
				Source: "src", SourceLabel: "资源源地址",
				Target: "data.url", TargetLabel: "统一·视频地址",
				IsPath: true,
			},
		},
	}
}

// buildAuthorPresets 构建作者信息字段映射预设。
func buildAuthorPresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "author_info",
		Label:       "作者信息字段",
		Description: "上游作者/用户对象字段映射到统一格式的 data.author.{name,id,avatar}",
		Presets: []FieldMappingPreset{
			{
				Source: "author.nickname", SourceLabel: "作者·昵称",
				Target: "data.author.name", TargetLabel: "统一·作者名称",
				IsPath: true,
			},
			{
				Source: "author.name", SourceLabel: "作者·名称",
				Target: "data.author.name", TargetLabel: "统一·作者名称",
				IsPath: true,
			},
			{
				Source: "author.username", SourceLabel: "作者·用户名",
				Target: "data.author.name", TargetLabel: "统一·作者名称",
				IsPath: true,
			},
			{
				Source: "author.user_name", SourceLabel: "作者·用户名（下划线）",
				Target: "data.author.name", TargetLabel: "统一·作者名称",
				IsPath: true,
			},
			{
				Source: "author.display_name", SourceLabel: "作者·显示名",
				Target: "data.author.name", TargetLabel: "统一·作者名称",
				IsPath: true,
			},
			{
				Source: "author.uid", SourceLabel: "作者·UID",
				Target: "data.author.id", TargetLabel: "统一·作者ID",
				IsPath: true,
			},
			{
				Source: "author.user_id", SourceLabel: "作者·用户ID",
				Target: "data.author.id", TargetLabel: "统一·作者ID",
				IsPath: true,
			},
			{
				Source: "author.id", SourceLabel: "作者·ID",
				Target: "data.author.id", TargetLabel: "统一·作者ID",
				IsPath: true,
			},
			{
				Source: "author.avatar", SourceLabel: "作者·头像",
				Target: "data.author.avatar", TargetLabel: "统一·作者头像",
				IsPath: true,
			},
			{
				Source: "author.avatar_url", SourceLabel: "作者·头像URL",
				Target: "data.author.avatar", TargetLabel: "统一·作者头像",
				IsPath: true,
			},
			{
				Source: "author.head", SourceLabel: "作者·头像（head）",
				Target: "data.author.avatar", TargetLabel: "统一·作者头像",
				IsPath: true,
			},
			{
				Source: "author.head_url", SourceLabel: "作者·头像URL（head）",
				Target: "data.author.avatar", TargetLabel: "统一·作者头像",
				IsPath: true,
			},
		},
	}
}

// buildMediaMetadataPresets 构建媒体元数据字段映射预设。
func buildMediaMetadataPresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "media_metadata",
		Label:       "媒体元数据字段",
		Description: "上游标题、描述、类型、时长、画质等字段映射到统一格式",
		Presets: []FieldMappingPreset{
			{
				Source: "title", SourceLabel: "标题",
				Target: "data.title", TargetLabel: "统一·标题",
				IsPath: true,
			},
			{
				Source: "desc", SourceLabel: "描述",
				Target: "data.desc", TargetLabel: "统一·描述",
				IsPath: true,
			},
			{
				Source: "description", SourceLabel: "描述（全拼）",
				Target: "data.desc", TargetLabel: "统一·描述",
				IsPath: true,
			},
			{
				Source: "caption", SourceLabel: "说明文字",
				Target: "data.desc", TargetLabel: "统一·描述",
				IsPath: true,
			},
			{
				Source: "text", SourceLabel: "文本内容",
				Target: "data.desc", TargetLabel: "统一·描述",
				IsPath: true,
			},
			{
				Source: "type", SourceLabel: "媒体类型",
				Target: "data.type", TargetLabel: "统一·媒体类型",
				IsPath: true,
			},
			{
				Source: "resource_type", SourceLabel: "资源类型",
				Target: "data.type", TargetLabel: "统一·媒体类型",
				IsPath: true,
			},
			{
				Source: "resourceType", SourceLabel: "资源类型（驼峰）",
				Target: "data.type", TargetLabel: "统一·媒体类型",
				IsPath: true,
			},
			{
				Source: "duration", SourceLabel: "时长（秒）",
				Target: "data.duration", TargetLabel: "统一·时长",
				IsPath: true,
			},
			{
				Source: "duration_seconds", SourceLabel: "时长（秒，下划线）",
				Target: "data.duration", TargetLabel: "统一·时长",
				IsPath: true,
			},
			{
				Source: "durationSeconds", SourceLabel: "时长（秒，驼峰）",
				Target: "data.duration", TargetLabel: "统一·时长",
				IsPath: true,
			},
			{
				Source: "quality", SourceLabel: "画质",
				Target: "data.quality", TargetLabel: "统一·画质",
				IsPath: true,
			},
			{
				Source: "resolution", SourceLabel: "分辨率",
				Target: "data.quality", TargetLabel: "统一·画质",
				IsPath: true,
			},
		},
	}
}

// buildMusicPresets 构建音乐字段映射预设。
func buildMusicPresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "music_info",
		Label:       "音乐/音频字段",
		Description: "上游音乐/音频对象字段映射到统一格式的 data.music.{title,author,url,cover}",
		Presets: []FieldMappingPreset{
			{
				Source: "music.title", SourceLabel: "音乐·标题",
				Target: "data.music.title", TargetLabel: "统一·音乐标题",
				IsPath: true,
			},
			{
				Source: "music.song_name", SourceLabel: "音乐·歌曲名",
				Target: "data.music.title", TargetLabel: "统一·音乐标题",
				IsPath: true,
			},
			{
				Source: "music.song", SourceLabel: "音乐·歌曲",
				Target: "data.music.title", TargetLabel: "统一·音乐标题",
				IsPath: true,
			},
			{
				Source: "music.author", SourceLabel: "音乐·作者",
				Target: "data.music.author", TargetLabel: "统一·音乐作者",
				IsPath: true,
			},
			{
				Source: "music.artist", SourceLabel: "音乐·艺术家",
				Target: "data.music.author", TargetLabel: "统一·音乐作者",
				IsPath: true,
			},
			{
				Source: "music.singer", SourceLabel: "音乐·歌手",
				Target: "data.music.author", TargetLabel: "统一·音乐作者",
				IsPath: true,
			},
			{
				Source: "music.url", SourceLabel: "音乐·地址",
				Target: "data.music.url", TargetLabel: "统一·音乐地址",
				IsPath: true,
			},
			{
				Source: "music.music_url", SourceLabel: "音乐·URL",
				Target: "data.music.url", TargetLabel: "统一·音乐地址",
				IsPath: true,
			},
			{
				Source: "music.play_url", SourceLabel: "音乐·播放链接",
				Target: "data.music.url", TargetLabel: "统一·音乐地址",
				IsPath: true,
			},
			{
				Source: "music.cover", SourceLabel: "音乐·封面",
				Target: "data.music.cover", TargetLabel: "统一·音乐封面",
				IsPath: true,
			},
		},
	}
}

// buildCoverImagePresets 构建封面和图片字段映射预设。
func buildCoverImagePresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "cover_image",
		Label:       "封面与图片字段",
		Description: "上游封面/缩略图/图片列表字段映射到统一格式",
		Presets: []FieldMappingPreset{
			{
				Source: "cover", SourceLabel: "封面图",
				Target: "data.cover", TargetLabel: "统一·封面图",
				IsPath: true,
			},
			{
				Source: "cover_url", SourceLabel: "封面图URL",
				Target: "data.cover", TargetLabel: "统一·封面图",
				IsPath: true,
			},
			{
				Source: "thumbnail", SourceLabel: "缩略图",
				Target: "data.cover", TargetLabel: "统一·封面图",
				IsPath: true,
			},
			{
				Source: "poster", SourceLabel: "海报/封帧",
				Target: "data.cover", TargetLabel: "统一·封面图",
				IsPath: true,
			},
			{
				Source: "thumb", SourceLabel: "小缩略图",
				Target: "data.cover", TargetLabel: "统一·封面图",
				IsPath: true,
			},
			{
				Source: "images", SourceLabel: "图片列表",
				Target: "data.images", TargetLabel: "统一·图片列表",
				IsPath: true,
			},
			{
				Source: "image_urls", SourceLabel: "图片URL列表",
				Target: "data.images", TargetLabel: "统一·图片列表",
				IsPath: true,
			},
			{
				Source: "imageUrls", SourceLabel: "图片URL列表（驼峰）",
				Target: "data.images", TargetLabel: "统一·图片列表",
				IsPath: true,
			},
			{
				Source: "pics", SourceLabel: "图片集",
				Target: "data.images", TargetLabel: "统一·图片列表",
				IsPath: true,
			},
			{
				Source: "photos", SourceLabel: "照片集",
				Target: "data.images", TargetLabel: "统一·图片列表",
				IsPath: true,
			},
		},
	}
}

// buildStatusEnvelopePresets 构建状态码和消息字段映射预设。
func buildStatusEnvelopePresets() FieldMappingPresetGroup {
	return FieldMappingPresetGroup{
		Key:         "status_envelope",
		Label:       "状态码与消息字段",
		Description: "上游响应中的状态码、消息字段映射到统一格式的 code 和 msg",
		Presets: []FieldMappingPreset{
			{
				Source: "code", SourceLabel: "状态码",
				Target: "code", TargetLabel: "统一·状态码",
				Note: "自动归一化：0/200~299→200",
			},
			{
				Source: "status_code", SourceLabel: "状态码（下划线）",
				Target: "code", TargetLabel: "统一·状态码",
			},
			{
				Source: "statusCode", SourceLabel: "状态码（驼峰）",
				Target: "code", TargetLabel: "统一·状态码",
			},
			{
				Source: "status", SourceLabel: "状态码",
				Target: "code", TargetLabel: "统一·状态码",
			},
			{
				Source: "msg", SourceLabel: "消息",
				Target: "msg", TargetLabel: "统一·消息",
			},
			{
				Source: "message", SourceLabel: "消息（全拼）",
				Target: "msg", TargetLabel: "统一·消息",
			},
			{
				Source: "errmsg", SourceLabel: "错误消息",
				Target: "msg", TargetLabel: "统一·消息",
			},
			{
				Source: "error_message", SourceLabel: "错误消息（下划线）",
				Target: "msg", TargetLabel: "统一·消息",
			},
			{
				Source: "errorMessage", SourceLabel: "错误消息（驼峰）",
				Target: "msg", TargetLabel: "统一·消息",
			},
			{
				Source: "success", SourceLabel: "成功标记",
				Target: "code", TargetLabel: "统一·状态码",
				Note: "布尔值：true→200 / false→500",
			},
		},
	}
}

// Ensure jsoncleaning.FieldMapping is imported for documentation.
var _ = jsoncleaning.FieldMapping{}
