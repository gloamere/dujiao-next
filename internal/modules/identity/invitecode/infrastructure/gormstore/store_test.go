package invitecodestore

import (
	"testing"
	"time"

	invitecodedomain "github.com/dujiao-next/internal/modules/identity/invitecode/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCreateBatchPreservesUsageLimits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&invitecodedomain.InviteCode{}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	if err := store.CreateBatch([]invitecodedomain.InviteCode{
		{Code: "UNLIMITED", MaxUses: 0, Status: invitecodedomain.StatusActive},
		{Code: "SINGLE", MaxUses: 1, Status: invitecodedomain.StatusActive},
		{Code: "LIMITED", MaxUses: 3, Status: invitecodedomain.StatusActive},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		code    string
		maxUses int
	}{
		{"UNLIMITED", 0},
		{"SINGLE", 1},
		{"LIMITED", 3},
	} {
		t.Run(tt.code, func(t *testing.T) {
			code, err := store.GetByCode(tt.code)
			if err != nil || code == nil {
				t.Fatalf("load code: code=%v err=%v", code, err)
			}
			if code.MaxUses != tt.maxUses {
				t.Fatalf("persisted max_uses=%d, want %d", code.MaxUses, tt.maxUses)
			}
			for attempt := 1; attempt <= 2; attempt++ {
				id, err := Consume(db, tt.code, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				wantUsable := tt.maxUses == 0 || attempt <= tt.maxUses
				if (id != 0) != wantUsable {
					t.Fatalf("consume attempt %d: id=%d, want usable=%v", attempt, id, wantUsable)
				}
			}
		})
	}
}
