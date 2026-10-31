package diag

// Check — один пункт диагностики (общий для платформ; форма JSON для фронта).
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail,omitempty"`
	Url    string `json:"url,omitempty"` // ссылка на решение
}
