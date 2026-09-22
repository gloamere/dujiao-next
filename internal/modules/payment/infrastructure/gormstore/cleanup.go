package gormstore

import (
	"github.com/dujiao-next/internal/constants"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (r *Store) cleanupCandidatesQuery(filter paymentcontract.CleanupFilter) *gorm.DB {
	allowedStatuses := filter.AllowedStatuses
	if len(allowedStatuses) == 0 {
		allowedStatuses = []string{constants.PaymentStatusFailed, constants.PaymentStatusExpired}
	}
	query := r.db.Model(&paymentdomain.Payment{}).
		Where("payments.deleted_at IS NULL AND payments.status IN ?", allowedStatuses)

	if filter.UserID != 0 {
		query = query.
			Joins("LEFT JOIN orders ON orders.id = payments.order_id").
			Joins("LEFT JOIN wallet_recharge_orders ON wallet_recharge_orders.payment_id = payments.id").
			Where("(orders.user_id = ? OR wallet_recharge_orders.user_id = ?)", filter.UserID, filter.UserID)
	}
	if filter.OrderID != 0 {
		query = query.Where("payments.order_id = ?", filter.OrderID)
	}
	if filter.ChannelID != 0 {
		query = query.Where("payments.channel_id = ?", filter.ChannelID)
	}
	if filter.ChannelType != "" {
		query = query.Where("payments.channel_type = ?", filter.ChannelType)
	}
	if filter.ProviderType != "" {
		query = query.Where("payments.provider_type = ?", filter.ProviderType)
	}
	if filter.Status != "" {
		query = query.Where("payments.status = ?", filter.Status)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("payments.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("payments.created_at <= ?", *filter.CreatedTo)
	}
	return query
}

// CountCleanupCandidates 统计当前授权状态范围内可清理的支付记录。
func (r *Store) CountCleanupCandidates(filter paymentcontract.CleanupFilter) (int64, error) {
	var count int64
	err := r.cleanupCandidatesQuery(filter).Distinct("payments.id").Count(&count).Error
	return count, err
}

// CleanupCandidates 软删除当前授权状态范围内符合条件的支付记录。
func (r *Store) CleanupCandidates(filter paymentcontract.CleanupFilter, deletedAt time.Time) (int64, error) {
	allowed := filter.AllowedStatuses
	if len(allowed) == 0 {
		allowed = []string{constants.PaymentStatusFailed, constants.PaymentStatusExpired}
	}
	var affected int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		txRepo := New(tx, r.guestCredentialSecret)
		var ids []uint
		candidates := txRepo.cleanupCandidatesQuery(filter).Select("payments.id")
		if err := tx.Model(&paymentdomain.Payment{}).Where("id IN (?) AND deleted_at IS NULL AND status IN ?", candidates, allowed).Clauses(clause.Locking{Strength: "UPDATE"}).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Model(&paymentdomain.Payment{}).
			Where("id IN ? AND deleted_at IS NULL AND status IN ?", ids, allowed).
			Updates(map[string]interface{}{"deleted_at": deletedAt, "updated_at": deletedAt})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}
