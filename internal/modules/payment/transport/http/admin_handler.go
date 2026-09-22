package paymenthttp

import (
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	auditlogapp "github.com/dujiao-next/internal/modules/auditlog/application"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"strconv"
	"strings"
	"time"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"

	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"

	"github.com/dujiao-next/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

const adminPaymentExportBatchSize = 500

// AdminPaymentListFilter 后台支付列表过滤条件。
type AdminPaymentListFilter struct {
	Page         int
	PageSize     int
	UserID       uint
	OrderID      uint
	ChannelID    uint
	ProviderType string
	ChannelType  string
	Status       string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	SkipCount    bool
	Lightweight  bool
}

// AdminPaymentQuery 后台支付查询端口。
type AdminPaymentQuery interface {
	ListPayments(filter AdminPaymentListFilter) ([]paymentdomain.Payment, int64, error)
	GetPayment(id uint) (*paymentdomain.Payment, error)
}

// AdminChannelLookup 后台支付渠道名称查询端口。
type AdminChannelLookup interface {
	ListByIDs(ids []uint) ([]paymentdomain.PaymentChannel, error)
}

// AdminOrderLookup 后台订单号查询端口。
type AdminOrderLookup interface {
	GetByIDs(ids []uint) ([]orderdomain.Order, error)
}

// AdminRechargeLookup 后台充值单元数据查询端口。
type AdminRechargeLookup interface {
	GetRechargeOrdersByPaymentIDs(paymentIDs []uint) ([]walletdomain.RechargeOrder, error)
}

// AdminPaymentItem 支付记录返回
type AdminPaymentItem struct {
	paymentdomain.Payment
	ChannelName        string `json:"channel_name"`
	DisplayChannelType string `json:"display_channel_type,omitempty"`
	OrderNo            string `json:"order_no,omitempty"`
	RechargeNo         string `json:"recharge_no,omitempty"`
	RechargeStatus     string `json:"recharge_status,omitempty"`
	RechargeUserID     uint   `json:"recharge_user_id,omitempty"`
}

type paymentRechargeMeta struct {
	RechargeNo string
	Status     string
	UserID     uint
}

// AdminHandler 处理后台支付只读 HTTP。
type AdminHandler struct {
	cleanup      AdminCleanup
	cleanupAudit CleanupAudit
	payments     AdminPaymentQuery
	channels     AdminChannelLookup
	orders       AdminOrderLookup
	recharge     AdminRechargeLookup
}

func NewAdminHandler(payments AdminPaymentQuery, channels AdminChannelLookup, orders AdminOrderLookup, recharge AdminRechargeLookup) *AdminHandler {
	if payments == nil {
		panic("payment admin handler: payments is nil")
	}
	return &AdminHandler{payments: payments, channels: channels, orders: orders, recharge: recharge}
}

// GetAdminPayments 获取支付记录列表
func (h *AdminHandler) GetAdminPayments(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	filter, err := buildAdminPaymentFilter(c, page, pageSize)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	payments, total, err := h.payments.ListPayments(filter)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	channelNameMap, err := h.resolvePaymentChannelNames(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	orderNoMap, err := h.resolvePaymentOrderNos(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	items := make([]AdminPaymentItem, 0, len(payments))
	for _, payment := range payments {
		rechargeMeta := rechargeMetaMap[payment.ID]
		safePayment := redactAdminPayment(payment)
		items = append(items, AdminPaymentItem{
			Payment:            safePayment,
			ChannelName:        channelNameMap[payment.ChannelID],
			DisplayChannelType: paymentDisplayChannelType(payment),
			OrderNo:            orderNoMap[payment.OrderID],
			RechargeNo:         rechargeMeta.RechargeNo,
			RechargeStatus:     rechargeMeta.Status,
			RechargeUserID:     rechargeMeta.UserID,
		})
	}

	response.SuccessWithPage(c, items, pagination)
}

// ExportAdminPayments 导出支付记录 CSV
func (h *AdminHandler) ExportAdminPayments(c *gin.Context) {
	filter, err := buildAdminPaymentFilter(c, 1, adminPaymentExportBatchSize)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	filter.SkipCount = true
	filter.Lightweight = true

	payments, _, err := h.payments.ListPayments(filter)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	filename := fmt.Sprintf("payments_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	writer := csv.NewWriter(c.Writer)
	if err := writer.Write([]string{
		"id",
		"order_id",
		"recharge_no",
		"recharge_status",
		"recharge_user_id",
		"channel_id",
		"provider_type",
		"channel_type",
		"display_channel_type",
		"status",
		"amount",
		"currency",
		"created_at",
		"paid_at",
		"expired_at",
		"provider_ref",
	}); err != nil {
		ginutil.RequestLog(c).Errorw("admin_payment_export_header_write_failed", "error", err)
		return
	}

	page := 1
	for {
		if len(payments) > 0 {
			if err := h.writeAdminPaymentCSVRows(writer, payments); err != nil {
				ginutil.RequestLog(c).Errorw("admin_payment_export_rows_write_failed", "page", page, "error", err)
				return
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				ginutil.RequestLog(c).Errorw("admin_payment_export_flush_failed", "page", page, "error", err)
				return
			}
		}
		if len(payments) < adminPaymentExportBatchSize {
			break
		}
		page++
		filter.Page = page
		payments, _, err = h.payments.ListPayments(filter)
		if err != nil {
			ginutil.RequestLog(c).Errorw("admin_payment_export_batch_fetch_failed", "page", page, "error", err)
			return
		}
	}
}

// GetAdminPayment 获取支付记录详情
func (h *AdminHandler) GetAdminPayment(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}

	payment, err := h.payments.GetPayment(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPaymentNotFound):
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		}
		return
	}

	channelNameMap, err := h.resolvePaymentChannelNames([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	orderNoMap, err := h.resolvePaymentOrderNos([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMeta := rechargeMetaMap[payment.ID]
	safePayment := redactAdminPayment(*payment)
	response.Success(c, AdminPaymentItem{
		Payment:            safePayment,
		ChannelName:        channelNameMap[payment.ChannelID],
		DisplayChannelType: paymentDisplayChannelType(*payment),
		OrderNo:            orderNoMap[payment.OrderID],
		RechargeNo:         rechargeMeta.RechargeNo,
		RechargeStatus:     rechargeMeta.Status,
		RechargeUserID:     rechargeMeta.UserID,
	})
}

func redactAdminPayment(payment paymentdomain.Payment) paymentdomain.Payment {
	payment.ProviderPayload = nil
	payment.PayURL = ""
	payment.QRCode = ""
	return payment
}

// paymentDisplayChannelType 提取后台支付记录的展示用渠道类型。
// CSV 导出的 lightweight 查询会把 provider_payload.display_channel_type 提取到 Payment.DisplayChannelType；
// 后台列表和详情会在响应前清空 ProviderPayload，因此必须在脱敏前从 payload 兜底读取。
func paymentDisplayChannelType(payment paymentdomain.Payment) string {
	if displayChannelType := strings.TrimSpace(payment.DisplayChannelType); displayChannelType != "" {
		return displayChannelType
	}
	value, ok := payment.ProviderPayload["display_channel_type"]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func formatTimeNullable(raw *time.Time) string {
	if raw == nil {
		return ""
	}
	return raw.Format(time.RFC3339)
}

func buildAdminPaymentFilter(c *gin.Context, page, pageSize int) (AdminPaymentListFilter, error) {
	orderID, err := ginutil.ParseQueryUint(c.Query("order_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}
	userID, err := ginutil.ParseQueryUint(c.Query("user_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}
	channelID, err := ginutil.ParseQueryUint(c.Query("channel_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}

	createdFrom, createdTo, err := ginutil.ParseQueryTimeRange(c, "created_from", "created_to")
	if err != nil {
		return AdminPaymentListFilter{}, err
	}

	return AdminPaymentListFilter{
		Page:         page,
		PageSize:     pageSize,
		UserID:       userID,
		OrderID:      orderID,
		ChannelID:    channelID,
		ProviderType: strings.TrimSpace(c.Query("provider_type")),
		ChannelType:  strings.TrimSpace(c.Query("channel_type")),
		Status:       strings.TrimSpace(c.Query("status")),
		CreatedFrom:  createdFrom,
		CreatedTo:    createdTo,
	}, nil
}

func (h *AdminHandler) writeAdminPaymentCSVRows(writer *csv.Writer, payments []paymentdomain.Payment) error {
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta(payments)
	if err != nil {
		return err
	}
	for _, payment := range payments {
		rechargeMeta := rechargeMetaMap[payment.ID]
		if err := writer.Write([]string{
			strconv.FormatUint(uint64(payment.ID), 10),
			strconv.FormatUint(uint64(payment.OrderID), 10),
			rechargeMeta.RechargeNo,
			rechargeMeta.Status,
			strconv.FormatUint(uint64(rechargeMeta.UserID), 10),
			strconv.FormatUint(uint64(payment.ChannelID), 10),
			payment.ProviderType,
			payment.ChannelType,
			paymentDisplayChannelType(payment),
			payment.Status,
			payment.Amount.String(),
			payment.Currency,
			payment.CreatedAt.Format(time.RFC3339),
			formatTimeNullable(payment.PaidAt),
			formatTimeNullable(payment.ExpiredAt),
			payment.ProviderRef,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *AdminHandler) resolvePaymentChannelNames(payments []paymentdomain.Payment) (map[uint]string, error) {
	channelIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.ChannelID == 0 {
			continue
		}
		if _, ok := seen[payment.ChannelID]; ok {
			continue
		}
		seen[payment.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, payment.ChannelID)
	}
	result := make(map[uint]string)
	if len(channelIDs) == 0 || h.channels == nil {
		return result, nil
	}
	channels, err := h.channels.ListByIDs(channelIDs)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		result[channel.ID] = channel.Name
	}
	return result, nil
}

func (h *AdminHandler) resolvePaymentOrderNos(payments []paymentdomain.Payment) (map[uint]string, error) {
	orderIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.OrderID == 0 {
			continue
		}
		if _, ok := seen[payment.OrderID]; ok {
			continue
		}
		seen[payment.OrderID] = struct{}{}
		orderIDs = append(orderIDs, payment.OrderID)
	}
	result := make(map[uint]string)
	if len(orderIDs) == 0 || h.orders == nil {
		return result, nil
	}
	orders, err := h.orders.GetByIDs(orderIDs)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		result[order.ID] = strings.TrimSpace(order.OrderNo)
	}
	return result, nil
}

func (h *AdminHandler) resolvePaymentRechargeMeta(payments []paymentdomain.Payment) (map[uint]paymentRechargeMeta, error) {
	paymentIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.ID == 0 {
			continue
		}
		if _, ok := seen[payment.ID]; ok {
			continue
		}
		seen[payment.ID] = struct{}{}
		paymentIDs = append(paymentIDs, payment.ID)
	}
	result := make(map[uint]paymentRechargeMeta)
	if len(paymentIDs) == 0 || h.recharge == nil {
		return result, nil
	}
	orders, err := h.recharge.GetRechargeOrdersByPaymentIDs(paymentIDs)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		result[order.PaymentID] = paymentRechargeMeta{
			RechargeNo: strings.TrimSpace(order.RechargeNo),
			Status:     strings.TrimSpace(order.Status),
			UserID:     order.UserID,
		}
	}
	return result, nil
}

type AdminCleanup interface {
	PreviewPaymentCleanup(paymentcontract.CleanupFilter) (int64, error)
	CleanupPayments(paymentcontract.CleanupFilter, time.Time) (int64, error)
}
type CleanupAudit interface {
	Record(auditlogapp.AuthzRecord) error
}

func (h *AdminHandler) WithCleanup(cleanup AdminCleanup, audit CleanupAudit) *AdminHandler {
	if cleanup == nil || audit == nil {
		panic("payment cleanup: required dependency is nil")
	}
	h.cleanup = cleanup
	h.cleanupAudit = audit
	return h
}
func (h *AdminHandler) recordCleanupAudit(c *gin.Context, input auditlogapp.AuthzRecord) {
	if err := h.cleanupAudit.Record(input); err != nil {
		ginutil.RequestLog(c).Errorw("payment_cleanup_audit_failed", "error", err, "operator_admin_id", input.OperatorAdminID)
	}
}

const adminPaymentCleanupConfirmation = "CLEAR_INVALID_PAYMENTS"
const adminPaymentSuperCleanupConfirmation = "DELETE_SUPER_PAYMENT_STATUS"
const adminPaymentCleanupScopeSuperStatus = "super_status"

var errAdminPaymentCleanupSuperRequired = errors.New("super admin is required for protected payment cleanup")

type adminPaymentCleanupRequest struct {
	Confirmation string `json:"confirmation" binding:"required"`
}

// PreviewAdminPaymentCleanup 预览当前筛选条件下可清理的支付记录数。
func (h *AdminHandler) PreviewAdminPaymentCleanup(c *gin.Context) {
	filter, eligibleStatuses, elevated, err := buildAdminPaymentCleanupFilter(c)
	if err != nil {
		if errors.Is(err, errAdminPaymentCleanupSuperRequired) {
			ginutil.RespondError(c, response.CodeForbidden, "error.forbidden", nil)
			return
		}
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	count, err := h.cleanup.PreviewPaymentCleanup(filter)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	response.Success(c, gin.H{
		"count":             count,
		"eligible_statuses": eligibleStatuses,
		"elevated":          elevated,
	})
}

// CleanupAdminPayments 软删除当前筛选条件下、管理员被授权清理的支付记录。
func (h *AdminHandler) CleanupAdminPayments(c *gin.Context) {
	var req adminPaymentCleanupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	filter, eligibleStatuses, elevated, err := buildAdminPaymentCleanupFilter(c)
	if err != nil {
		if errors.Is(err, errAdminPaymentCleanupSuperRequired) {
			ginutil.RespondError(c, response.CodeForbidden, "error.forbidden", nil)
			return
		}
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	expectedConfirmation := adminPaymentCleanupConfirmation
	if elevated {
		expectedConfirmation = adminPaymentSuperCleanupConfirmation
	}
	if strings.TrimSpace(req.Confirmation) != expectedConfirmation {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}

	affected, err := h.cleanup.CleanupPayments(filter, time.Now())
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_update_failed", err)
		return
	}

	h.recordCleanupAudit(c, auditlogapp.AuthzRecord{
		OperatorAdminID:  c.GetUint("admin_id"),
		OperatorUsername: strings.TrimSpace(c.GetString("username")),
		Action:           "payment_records_cleanup",
		Object:           "/admin/payments/cleanup",
		Method:           "POST",
		RequestID:        strings.TrimSpace(c.GetString("request_id")),
		Detail: jsonmap.JSON{
			"affected":          affected,
			"eligible_statuses": eligibleStatuses,
			"elevated":          elevated,
			"filters": jsonmap.JSON{
				"user_id":       filter.UserID,
				"order_id":      filter.OrderID,
				"channel_id":    filter.ChannelID,
				"provider_type": filter.ProviderType,
				"status":        filter.Status,
				"created_from":  formatTimeNullable(filter.CreatedFrom),
				"created_to":    formatTimeNullable(filter.CreatedTo),
			},
		},
	})

	ginutil.RequestLog(c).Infow("admin_payment_records_cleaned",
		"operator_admin_id", c.GetUint("admin_id"),
		"affected", affected,
		"eligible_statuses", eligibleStatuses,
		"elevated", elevated,
	)
	response.Success(c, gin.H{"affected": affected})
}

func buildAdminPaymentCleanupFilter(c *gin.Context) (paymentcontract.CleanupFilter, []string, bool, error) {
	listFilter, err := buildAdminPaymentFilter(c, 1, 1)
	if err != nil {
		return paymentcontract.CleanupFilter{}, nil, false, err
	}
	eligibleStatuses := []string{constants.PaymentStatusFailed, constants.PaymentStatusExpired}
	elevated := false
	scope := strings.TrimSpace(c.Query("cleanup_scope"))
	if scope != "" && scope != adminPaymentCleanupScopeSuperStatus {
		return paymentcontract.CleanupFilter{}, nil, false, fmt.Errorf("invalid cleanup scope")
	}
	if scope == adminPaymentCleanupScopeSuperStatus {
		if !ginutil.IsSuperAdmin(c) {
			return paymentcontract.CleanupFilter{}, nil, false, errAdminPaymentCleanupSuperRequired
		}
		if !isProtectedPaymentCleanupStatus(listFilter.Status) {
			return paymentcontract.CleanupFilter{}, nil, false, fmt.Errorf("protected cleanup requires an explicit status")
		}
		eligibleStatuses = []string{listFilter.Status}
		elevated = true
	}
	return paymentcontract.CleanupFilter{
		UserID:          listFilter.UserID,
		OrderID:         listFilter.OrderID,
		ChannelID:       listFilter.ChannelID,
		ProviderType:    listFilter.ProviderType,
		ChannelType:     listFilter.ChannelType,
		Status:          listFilter.Status,
		CreatedFrom:     listFilter.CreatedFrom,
		CreatedTo:       listFilter.CreatedTo,
		AllowedStatuses: eligibleStatuses,
	}, eligibleStatuses, elevated, nil
}

func isProtectedPaymentCleanupStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case constants.PaymentStatusSuccess, constants.PaymentStatusPending, constants.PaymentStatusInitiated:
		return true
	default:
		return false
	}
}
