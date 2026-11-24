package upload

import "sort"

const attachmentMultipartHeadroom = 64 << 10
const attachmentPolicyMiB = 1 << 20

// AttachmentPolicy describes limits that fit inside the HTTP server's actual
// request envelope. It does not raise that envelope or trust browser metadata.
type AttachmentPolicy struct {
	MaxBytes          int64    `json:"maxBytes"`
	AllowedExtensions []string `json:"allowedExtensions"`
}

func AttachmentPolicyForBodyLimit(maxRequestBodyBytes int) AttachmentPolicy {
	limit := int64(0)
	if maxRequestBodyBytes > attachmentMultipartHeadroom {
		limit = (int64(maxRequestBodyBytes) - attachmentMultipartHeadroom) / attachmentPolicyMiB * attachmentPolicyMiB
		if limit > MaxAttachmentSize {
			limit = MaxAttachmentSize
		}
	}
	extensions := make([]string, 0, len(allowedAttachmentExt))
	for extension := range allowedAttachmentExt {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	return AttachmentPolicy{MaxBytes: limit, AllowedExtensions: extensions}
}
