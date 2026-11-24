package interfaceapi

// UnifiedFormatSchema 返回短视频统一 JSON 格式的完整 JSON Schema 描述。
// 用于前端渲染字段映射表单时的格式参考和输入校验提示。
// GET /system-api/interface/unified-format-schema
func UnifiedFormatSchema() map[string]any {
	return map[string]any{
		"description": "短视频解析统一 JSON 响应格式（videojson 输出格式）",
		"envelope": map[string]any{
			"code": map[string]any{
				"type":        "int",
				"description": "状态码，200 表示成功",
				"jsonTag":     "code",
			},
			"msg": map[string]any{
				"type":        "string",
				"description": "状态消息",
				"jsonTag":     "msg",
			},
			"data": map[string]any{
				"type":        "object",
				"description": "解析结果数据",
				"jsonTag":     "data",
				"fields": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "媒体类型：video（视频）/ image（图片）/ live（实况）",
						"jsonTag":     "type",
						"enum":        []string{"video", "image", "live"},
					},
					"title": map[string]any{
						"type":        "string",
						"description": "标题/文案",
						"jsonTag":     "title",
					},
					"desc": map[string]any{
						"type":        "string",
						"description": "描述/简介/说明",
						"jsonTag":     "desc",
					},
					"author": map[string]any{
						"type":        "object",
						"description": "作者信息",
						"jsonTag":     "author",
						"fields": map[string]any{
							"name": map[string]any{
								"type":        "string",
								"description": "作者名称/昵称",
								"jsonTag":     "name",
							},
							"id": map[string]any{
								"type":        "string",
								"description": "作者 ID / UID",
								"jsonTag":     "id",
							},
							"avatar": map[string]any{
								"type":        "string",
								"description": "作者头像 URL",
								"jsonTag":     "avatar",
							},
						},
					},
					"cover": map[string]any{
						"type":        "string",
						"description": "封面图 URL",
						"jsonTag":     "cover",
					},
					"url": map[string]any{
						"type":        "string",
						"description": "视频/媒体资源 URL（无视频/图片时为空字符串）",
						"jsonTag":     "url",
					},
					"quality": map[string]any{
						"type":        "string",
						"description": "视频画质/分辨率（如 720p、1080p、4K）",
						"jsonTag":     "quality",
					},
					"duration": map[string]any{
						"type":        "number|null",
						"description": "时长（秒，无时长时为 null）",
						"jsonTag":     "duration",
					},
					"images": map[string]any{
						"type":        "array[string]",
						"description": "图片 URL 列表（图集/画廊模式）",
						"jsonTag":     "images",
					},
					"live_photo": map[string]any{
						"type":        "array[object]",
						"description": "Live Photo 数据 [{image, video}]",
						"jsonTag":     "live_photo",
					},
					"music": map[string]any{
						"type":        "object",
						"description": "音乐/音频信息",
						"jsonTag":     "music",
						"fields": map[string]any{
							"title": map[string]any{
								"type":        "string",
								"description": "音乐标题/歌曲名",
								"jsonTag":     "title",
							},
							"author": map[string]any{
								"type":        "string",
								"description": "音乐作者/歌手",
								"jsonTag":     "author",
							},
							"url": map[string]any{
								"type":        "string",
								"description": "音乐/音频 URL",
								"jsonTag":     "url",
							},
							"cover": map[string]any{
								"type":        "string",
								"description": "音乐封面 URL",
								"jsonTag":     "cover",
							},
						},
					},
					"extra": map[string]any{
						"type":        "object",
						"description": "额外字段（未被归类的上游数据）",
						"jsonTag":     "extra",
					},
					"video_backup": map[string]any{
						"type":        "array[object]",
						"description": "视频备选地址列表（多线路/多清晰度，仅视频类型时出现）",
						"jsonTag":     "video_backup",
					},
				},
			},
		},
		"jsonPathSyntax": map[string]any{
			"description": "字段映射中 JSON 路径语法说明",
			"examples": []map[string]any{
				{
					"type":        "普通字段名",
					"syntax":      "field_name",
					"description": "使用 plain 字段名时，会在所有嵌套层级中递归替换同名键。",
					"example":     []map[string]string{{"source": "video_url", "target": "url"}},
				},
				{
					"type":        "JSON 路径（点号）",
					"syntax":      "parent.child",
					"description": "使用点号分隔的路径在 JSON 结构中精确定位。以 $ 开头可选。",
					"example":     []map[string]string{{"source": "data.author.nickname", "target": "data.author.name"}},
				},
				{
					"type":        "数组索引",
					"syntax":      "array.0.field",
					"description": "使用数字索引（从 0 开始）定位数组元素中的字段。",
					"example":     []map[string]string{{"source": "data.images.0", "target": "data.cover"}},
				},
				{
					"type":        "数组通配符",
					"syntax":      "items[].field",
					"description": "使用 [] 或 [*] 遍历数组中的每个元素。源和目标必须使用相同数量的通配符。",
					"example":     []map[string]string{{"source": "items[].media_url", "target": "data.images[]"}},
				},
			},
		},
	}
}
