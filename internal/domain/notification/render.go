package notification

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/emaillang"
)

type permanentError struct {
	err error
}

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func Render(
	templateKey string,
	language string,
	siteName string,
	payload map[string]any,
) (RenderedMessage, error) {
	language = normalizeLanguage(language)
	siteName = cleanText(siteName, 64)
	if siteName == "" {
		siteName = "BugPk-Api"
	}
	value := payloadReader{values: payload}
	switch strings.TrimSpace(templateKey) {
	case TemplateTicketCreated:
		return renderTicketCreated(language, siteName, value), nil
	case TemplateTicketUserReply:
		return renderTicketUserReply(language, siteName, value), nil
	case TemplateTicketUserClosed:
		return renderTicketUserClosed(language, siteName, value), nil
	case TemplateTicketStaffReply:
		return renderTicketStaffReply(language, siteName, value), nil
	case TemplateTicketStatus:
		return renderTicketStatus(language, siteName, value), nil
	case TemplateFriendReceived:
		return renderFriendReceived(language, siteName, value), nil
	case TemplateFriendPending:
		return renderFriendPendingReview(language, siteName, value), nil
	case TemplateFriendApproved:
		return renderFriendApproved(language, siteName, value), nil
	case TemplateFriendRejected:
		return renderFriendRejected(language, siteName, value), nil
	case TemplateFriendDeleted:
		return renderFriendDeleted(language, siteName, value), nil
	case TemplatePaymentPaid:
		return renderPaymentPaid(language, siteName, value), nil
	case TemplateBroadcast:
		return renderBroadcast(language, siteName, value), nil
	default:
		return RenderedMessage{}, permanentError{err: fmt.Errorf("unsupported notification template %q", templateKey)}
	}
}

func renderTicketCreated(lang, site string, value payloadReader) RenderedMessage {
	number := value.text("ticketNo", 64)
	subject := value.text("subject", 128)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] New ticket %s", site, number),
			Body: strings.Join([]string{
				"A user submitted a new support ticket.",
				"",
				"Ticket: " + number,
				"Subject: " + subject,
				"Category: " + ticketCategoryLabel(value.text("category", 32), lang),
				"Priority: " + ticketPriorityLabel(value.text("priority", 32), lang),
				"Requester: " + value.text("requester", 128),
				"Summary: " + value.text("preview", 500),
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 新工单 %s", site, number),
		Body: strings.Join([]string{
			"有用户提交了新的支持工单。",
			"",
			"工单编号：" + number,
			"工单标题：" + subject,
			"问题分类：" + ticketCategoryLabel(value.text("category", 32), lang),
			"优先级：" + ticketPriorityLabel(value.text("priority", 32), lang),
			"提交账号：" + value.text("requester", 128),
			"内容摘要：" + value.text("preview", 500),
		}, "\n"),
	}
}

func renderTicketUserReply(lang, site string, value payloadReader) RenderedMessage {
	number := value.text("ticketNo", 64)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] User replied to %s", site, number),
			Body: strings.Join([]string{
				"A user added information to a support ticket.",
				"",
				"Ticket: " + number,
				"Subject: " + value.text("subject", 128),
				"Requester: " + value.text("requester", 128),
				"Message: " + value.text("preview", 500),
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 用户补充工单 %s", site, number),
		Body: strings.Join([]string{
			"用户补充了工单信息。",
			"",
			"工单编号：" + number,
			"工单标题：" + value.text("subject", 128),
			"提交账号：" + value.text("requester", 128),
			"回复摘要：" + value.text("preview", 500),
		}, "\n"),
	}
}

func renderTicketUserClosed(lang, site string, value payloadReader) RenderedMessage {
	number := value.text("ticketNo", 64)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] User closed ticket %s", site, number),
			Body: strings.Join([]string{
				"A user closed a support ticket.",
				"",
				"Ticket: " + number,
				"Subject: " + value.text("subject", 128),
				"Requester: " + value.text("requester", 128),
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 用户关闭工单 %s", site, number),
		Body: strings.Join([]string{
			"用户关闭了一条支持工单。",
			"",
			"工单编号：" + number,
			"工单标题：" + value.text("subject", 128),
			"提交账号：" + value.text("requester", 128),
		}, "\n"),
	}
}

func renderTicketStaffReply(lang, site string, value payloadReader) RenderedMessage {
	number := value.text("ticketNo", 64)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] New reply on ticket %s", site, number),
			Body: strings.Join([]string{
				"Support replied to your ticket.",
				"",
				"Ticket: " + number,
				"Subject: " + value.text("subject", 128),
				"Reply: " + value.text("preview", 500),
				"Sign in to the support center to view the full conversation.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 工单 %s 收到新回复", site, number),
		Body: strings.Join([]string{
			"支持人员回复了您的工单。",
			"",
			"工单编号：" + number,
			"工单标题：" + value.text("subject", 128),
			"回复摘要：" + value.text("preview", 500),
			"请登录工单中心查看完整沟通记录。",
		}, "\n"),
	}
}

func renderTicketStatus(lang, site string, value payloadReader) RenderedMessage {
	number := value.text("ticketNo", 64)
	status := ticketStatusLabel(value.text("status", 32), lang)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Ticket %s status updated", site, number),
			Body: strings.Join([]string{
				"The status of your support ticket changed.",
				"",
				"Ticket: " + number,
				"Subject: " + value.text("subject", 128),
				"Current status: " + status,
				"Sign in to the support center for details.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 工单 %s 状态已更新", site, number),
		Body: strings.Join([]string{
			"您的支持工单状态发生了变化。",
			"",
			"工单编号：" + number,
			"工单标题：" + value.text("subject", 128),
			"当前状态：" + status,
			"请登录工单中心查看详情。",
		}, "\n"),
	}
}

func renderFriendReceived(lang, site string, value payloadReader) RenderedMessage {
	kind := friendLinkChangeLabel(value.text("changeKind", 16), lang)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Friend link request received", site),
			Body: strings.Join([]string{
				"We have received your " + kind + ".",
				"",
				"Site: " + value.text("siteName", 100),
				"URL: " + value.text("siteUrl", 1024),
				"We will email you again once the review is complete.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 友链请求已收到", site),
		Body: strings.Join([]string{
			"我们已收到您的" + kind + "，审核完成后会再次通过邮件通知您。",
			"",
			"站点名称：" + value.text("siteName", 100),
			"站点地址：" + value.text("siteUrl", 1024),
		}, "\n"),
	}
}

func renderFriendPendingReview(lang, site string, value payloadReader) RenderedMessage {
	kind := friendLinkChangeLabel(value.text("changeKind", 16), lang)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] New friend link request awaiting review", site),
			Body: strings.Join([]string{
				"A new " + kind + " is awaiting review.",
				"",
				"Site: " + value.text("siteName", 100),
				"URL: " + value.text("siteUrl", 1024),
				"Applicant: " + value.text("applicant", 128),
				"Contact email: " + value.text("contactEmail", 128),
				"Please sign in to the admin console to review it.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 新的友链请求待审核", site),
		Body: strings.Join([]string{
			"有新的" + kind + "等待审核。",
			"",
			"站点名称：" + value.text("siteName", 100),
			"站点地址：" + value.text("siteUrl", 1024),
			"申请人：" + value.text("applicant", 128),
			"联系邮箱：" + value.text("contactEmail", 128),
			"请登录后台友链审核页面处理。",
		}, "\n"),
	}
}

func friendLinkChangeLabel(value, lang string) string {
	labels := map[string][2]string{
		"NEW":    {"友链申请", "friend link application"},
		"EDIT":   {"友链信息修改请求", "friend link change request"},
		"REMOVE": {"友链撤下请求", "friend link removal request"},
	}
	return localizedLookup(labels, value, lang)
}

func renderFriendApproved(lang, site string, value payloadReader) RenderedMessage {
	name := value.text("siteName", 100)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Friend link application approved", site),
			Body: strings.Join([]string{
				"Your friend link application has been approved.",
				"",
				"Site: " + name,
				"URL: " + value.text("siteUrl", 1024),
				"Thank you for exchanging links with " + site + ".",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 友链申请已通过", site),
		Body: strings.Join([]string{
			"您的友链申请已审核通过。",
			"",
			"站点名称：" + name,
			"站点地址：" + value.text("siteUrl", 1024),
			"感谢您与 " + site + " 交换友情链接。",
		}, "\n"),
	}
}

func renderFriendRejected(lang, site string, value payloadReader) RenderedMessage {
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Friend link application update", site),
			Body: strings.Join([]string{
				"Your friend link application was not approved.",
				"",
				"Site: " + value.text("siteName", 100),
				"Reason: " + value.text("reason", 1000),
				"You may update the site and submit another application.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 友链申请审核结果", site),
		Body: strings.Join([]string{
			"您的友链申请未通过审核。",
			"",
			"站点名称：" + value.text("siteName", 100),
			"未通过原因：" + value.text("reason", 1000),
			"您可以调整站点信息后重新提交申请。",
		}, "\n"),
	}
}

func renderFriendDeleted(lang, site string, value payloadReader) RenderedMessage {
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Friend link removed", site),
			Body: strings.Join([]string{
				"Your friend link has been removed.",
				"",
				"Site: " + value.text("siteName", 100),
				"Reason: " + value.text("reason", 1000),
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 友情链接已移除", site),
		Body: strings.Join([]string{
			"您的友情链接已从本站移除。",
			"",
			"站点名称：" + value.text("siteName", 100),
			"移除原因：" + value.text("reason", 1000),
		}, "\n"),
	}
}

func renderPaymentPaid(lang, site string, value payloadReader) RenderedMessage {
	orderNo := value.text("orderNo", 64)
	subject := value.text("subject", 128)
	amount := value.text("amount", 32)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] Payment received for order %s", site, orderNo),
			Body: strings.Join([]string{
				"Your payment was successful and the order has been settled.",
				"",
				"Order: " + orderNo,
				"Item: " + subject,
				"Amount: " + amount,
				"Paid at: " + value.text("paidAt", 32),
				"Sign in to view your order history.",
			}, "\n"),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] 订单 %s 支付成功", site, orderNo),
		Body: strings.Join([]string{
			"您的支付已完成，订单已到账。",
			"",
			"订单编号：" + orderNo,
			"商品名称：" + subject,
			"支付金额：" + amount,
			"支付时间：" + value.text("paidAt", 32),
			"请登录控制台查看订单详情。",
		}, "\n"),
	}
}

func renderBroadcast(lang, site string, value payloadReader) RenderedMessage {
	title := value.text("title", 128)
	body := value.text("body", 4000)
	if lang == "en" {
		return RenderedMessage{
			Subject: fmt.Sprintf("[%s] %s", site, title),
			Body: strings.TrimSpace(strings.Join([]string{
				body,
				"",
				"Sign in to view your message center.",
			}, "\n")),
		}
	}
	return RenderedMessage{
		Subject: fmt.Sprintf("[%s] %s", site, title),
		Body: strings.TrimSpace(strings.Join([]string{
			body,
			"",
			"请登录控制台查看消息中心。",
		}, "\n")),
	}
}

type payloadReader struct {
	values map[string]any
}

func (p payloadReader) text(key string, maxRunes int) string {
	if p.values == nil {
		return "-"
	}
	value := cleanText(fmt.Sprint(p.values[key]), maxRunes)
	if value == "" || value == "<nil>" {
		return "-"
	}
	return value
}

func cleanText(value string, maxRunes int) string {
	value = strings.TrimSpace(strings.NewReplacer("\x00", "", "\r", " ", "\n", " ").Replace(value))
	value = strings.Join(strings.Fields(value), " ")
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		runes := []rune(value)
		value = string(runes[:maxRunes]) + "…"
	}
	return value
}

func normalizeLanguage(value string) string {
	return emaillang.Resolve(value, "", "", "")
}

func ticketStatusLabel(value, lang string) string {
	labels := map[string][2]string{
		"OPEN":         {"待处理", "Open"},
		"PROCESSING":   {"处理中", "In progress"},
		"WAITING_USER": {"待用户回复", "Waiting for user"},
		"RESOLVED":     {"已解决", "Resolved"},
		"CLOSED":       {"已关闭", "Closed"},
	}
	return localizedLookup(labels, value, lang)
}

func ticketCategoryLabel(value, lang string) string {
	labels := map[string][2]string{
		"GENERAL": {"一般咨询", "General"},
		"API":     {"接口问题", "API issue"},
		"BILLING": {"计费与订单", "Billing and orders"},
		"ACCOUNT": {"账号问题", "Account"},
		"BUG":     {"故障反馈", "Bug report"},
		"OTHER":   {"其他", "Other"},
	}
	return localizedLookup(labels, value, lang)
}

func ticketPriorityLabel(value, lang string) string {
	labels := map[string][2]string{
		"LOW":    {"低", "Low"},
		"NORMAL": {"普通", "Normal"},
		"HIGH":   {"高", "High"},
		"URGENT": {"紧急", "Urgent"},
	}
	return localizedLookup(labels, value, lang)
}

func localizedLookup(labels map[string][2]string, value, lang string) string {
	key := strings.ToUpper(strings.TrimSpace(value))
	label, ok := labels[key]
	if !ok {
		return cleanText(value, 64)
	}
	if lang == "en" {
		return label[1]
	}
	return label[0]
}

func isPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}
