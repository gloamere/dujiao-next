package application_test

import (
	"errors"
	"fmt"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	"strings"
	"testing"
	"time"

	. "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentgormstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"

	"github.com/dujiao-next/internal/constants"
	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	"github.com/dujiao-next/internal/platform/database/gormdb"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupFulfillmentServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:fulfillment_service_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&fulfillmentdomain.Fulfillment{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func TestCreateAutoFulfillmentRespectsSKUBoundary(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "FULFILL-SKU-001",
		UserID:                  1,
		Status:                  constants.OrderStatusPaid,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	orderItem := &orderdomain.OrderItem{
		OrderID:         order.ID,
		ProductID:       100,
		SKUID:           1001,
		TitleJSON:       jsonmap.JSON{"zh-CN": "测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(10)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(10)),
		FulfillmentType: constants.FulfillmentTypeAuto,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(orderItem).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}

	secretTarget := &cardsecretdomain.Secret{
		ProductID: 100,
		SKUID:     1001,
		Secret:    "SECRET-SKU-1001",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secretTarget).Error; err != nil {
		t.Fatalf("create target secret failed: %v", err)
	}
	secretOther := &cardsecretdomain.Secret{
		ProductID: 100,
		SKUID:     1002,
		Secret:    "SECRET-SKU-1002",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secretOther).Error; err != nil {
		t.Fatalf("create other secret failed: %v", err)
	}

	svc := New(Options{
		OrderStore:       ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes"),
		FulfillmentStore: fulfillmentgormstore.New(db),
	})

	result, err := svc.CreateAuto(order.ID)
	if err != nil {
		t.Fatalf("create auto fulfillment failed: %v", err)
	}
	if result == nil {
		t.Fatalf("fulfillment should not be nil")
	}
	if !strings.Contains(result.Payload, "SECRET-SKU-1001") {
		t.Fatalf("payload should contain target sku secret, got: %s", result.Payload)
	}
	if strings.Contains(result.Payload, "SECRET-SKU-1002") {
		t.Fatalf("payload should not contain other sku secret, got: %s", result.Payload)
	}

	var targetAfter cardsecretdomain.Secret
	if err := db.First(&targetAfter, secretTarget.ID).Error; err != nil {
		t.Fatalf("query target secret failed: %v", err)
	}
	if targetAfter.Status != cardsecretdomain.StatusUsed {
		t.Fatalf("target secret status want used got %s", targetAfter.Status)
	}

	var otherAfter cardsecretdomain.Secret
	if err := db.First(&otherAfter, secretOther.ID).Error; err != nil {
		t.Fatalf("query other secret failed: %v", err)
	}
	if otherAfter.Status != cardsecretdomain.StatusAvailable {
		t.Fatalf("other secret status should stay available got %s", otherAfter.Status)
	}

	var orderAfter orderdomain.Order
	if err := db.First(&orderAfter, order.ID).Error; err != nil {
		t.Fatalf("query order failed: %v", err)
	}
	if orderAfter.Status != constants.OrderStatusCompleted {
		t.Fatalf("order status want completed got %s", orderAfter.Status)
	}
}

type statusChangingOrderStore struct {
	ordercontract.Store
	db      *gorm.DB
	orderID uint
}

func (s *statusChangingOrderStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	if err := s.db.Model(&orderdomain.Order{}).Where("id = ?", s.orderID).Update("status", constants.OrderStatusCanceled).Error; err != nil {
		return err
	}
	return s.Store.WithinTransaction(fn)
}
func TestCreateAutoRechecksOrderStatusInsideTransaction(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	order := orderdomain.Order{OrderNo: "CANCELED-BEFORE-LOCK", Status: constants.OrderStatusPaid, Currency: "CNY"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: order.ID, ProductID: 100, SKUID: 1001, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "Test"}, FulfillmentType: constants.FulfillmentTypeAuto}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	card := cardsecretdomain.Secret{ProductID: 100, SKUID: 1001, Secret: "NOT-ISSUED", Status: cardsecretdomain.StatusAvailable}
	if err := db.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	store := &statusChangingOrderStore{Store: ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes"), db: db, orderID: order.ID}
	svc := New(Options{OrderStore: store, FulfillmentStore: fulfillmentgormstore.New(db)})
	if _, err := svc.CreateAuto(order.ID); !errors.Is(err, ErrOrderStatusInvalid) {
		t.Fatalf("want invalid order status, got %v", err)
	}
	var count int64
	db.Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count)
	if count != 0 {
		t.Fatalf("canceled order got fulfillment")
	}
	if err := db.First(&card, card.ID).Error; err != nil {
		t.Fatal(err)
	}
	if card.Status != cardsecretdomain.StatusAvailable {
		t.Fatalf("card consumed")
	}
}
