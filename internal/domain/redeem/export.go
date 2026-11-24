package redeem

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// 导出列表头（顺序即列序）。
var exportHeaders = []string{"兑换码", "类型", "状态", "来源", "批次", "核销用户", "核销时间", "生成时间"}

// writeCodeWorkbook 写出 xlsx 工作簿并返回建议文件名。
// 单元格公式注入转义：以 = + - @ 开头的文本前置单引号（与 observability
// CSV 导出 safeCSVCell 同口径，M-6）。
func writeCodeWorkbook(typeItem typeRow, rows []codeRow, out io.Writer) (string, error) {
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	sheet := file.GetSheetName(0)
	for index, header := range exportHeaders {
		cell, err := excelize.CoordinatesToCellName(index+1, 1)
		if err != nil {
			return "", err
		}
		if err := file.SetCellValue(sheet, cell, header); err != nil {
			return "", err
		}
	}
	for rowIndex, row := range rows {
		line := rowIndex + 2
		values := []string{
			row.CodePlain,
			typeItem.Name,
			codeStatusText(row.Status),
			batchSourceText(row.Source),
			nullInt64String(row.BatchID),
			nullInt64String(row.UsedBy),
			formatNullTime(row.UsedAt),
			formatTime(row.CreatedAt),
		}
		for colIndex, value := range values {
			cell, err := excelize.CoordinatesToCellName(colIndex+1, line)
			if err != nil {
				return "", err
			}
			if err := file.SetCellValue(sheet, cell, safeCell(value)); err != nil {
				return "", err
			}
		}
	}
	// 明文列加宽，便于管理员直接阅读/复制。
	if err := file.SetColWidth(sheet, "A", "A", 26); err != nil {
		return "", err
	}
	if err := file.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1}); err != nil {
		return "", err
	}
	if _, err := file.WriteTo(out); err != nil {
		return "", err
	}
	return exportFilename(typeItem), nil
}

func exportFilename(typeItem typeRow) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, typeItem.Code)
	if safe == "" {
		safe = "redeem"
	}
	return fmt.Sprintf("redeem-codes-%s-%s.xlsx", safe, time.Now().Format("20060102150405"))
}

func codeStatusText(status int) string {
	switch status {
	case CodeStatusUnused:
		return "未使用"
	case CodeStatusConsumed:
		return "已使用"
	case CodeStatusVoided:
		return "已作废"
	default:
		return strconv.Itoa(status)
	}
}

func batchSourceText(source int) string {
	if source == SourceOpenAPI {
		return "开放API生成"
	}
	return "管理端生成"
}

// safeCell 公式注入转义：= + - @ 开头的文本前置单引号，Excel 按纯文本解析。
func safeCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	}
	return value
}
