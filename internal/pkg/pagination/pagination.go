package pagination

const (
	DefaultPageSize = 20
	MaxPage         = 1000
)

func Normalize(page, pageSize, maxPageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > MaxPage {
		page = MaxPage
	}
	if maxPageSize < 1 {
		maxPageSize = DefaultPageSize
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func Offset(page, pageSize int) int {
	page, pageSize = Normalize(page, pageSize, pageSize)
	return (page - 1) * pageSize
}
