package contract

import "time"

// CleanupFilter 管理端清理支付记录的过滤条件。
// AllowedStatuses 由管理端处理器根据管理员级别生成；为空时仓储默认仅允许 failed/expired。
type CleanupFilter struct {
	UserID          uint
	OrderID         uint
	ChannelID       uint
	ChannelType     string
	ProviderType    string
	Status          string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
	AllowedStatuses []string
}

type CleanupStore interface {
	CountCleanupCandidates(CleanupFilter) (int64, error)
	CleanupCandidates(CleanupFilter, time.Time) (int64, error)
}
