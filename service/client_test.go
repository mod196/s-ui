package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
)

const gib int64 = 1024 * 1024 * 1024

func setupClientTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "s-ui.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("init db: %v", err)
	}
}

func saveClient(t *testing.T, svc *ClientService, action string, client model.Client) model.Client {
	t.Helper()
	data, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("marshal client: %v", err)
	}
	tx := database.GetDB().Begin()
	_, err = svc.Save(tx, action, data, "example.com")
	if err != nil {
		tx.Rollback()
		t.Fatalf("save client: %v", err)
	}
	if err = tx.Commit().Error; err != nil {
		t.Fatalf("commit client: %v", err)
	}
	var saved model.Client
	if err = database.GetDB().Where("name = ?", client.Name).First(&saved).Error; err != nil {
		t.Fatalf("load client: %v", err)
	}
	return saved
}

func baseClient(name string) model.Client {
	return model.Client{
		Enable:   true,
		Name:     name,
		Config:   json.RawMessage(`{}`),
		Inbounds: json.RawMessage(`[]`),
		Links:    json.RawMessage(`[]`),
		Volume:   200 * gib,
	}
}

func TestMonthlyQuotaDefaultsForNewClient(t *testing.T) {
	setupClientTestDB(t)
	svc := &ClientService{}

	client := saveClient(t, svc, "new", baseClient("leo"))

	if !client.AutoReset {
		t.Fatal("expected monthly auto reset to be enabled")
	}
	if client.ResetDays != defaultMonthlyResetDays {
		t.Fatalf("expected reset days %d, got %d", defaultMonthlyResetDays, client.ResetDays)
	}
	if client.NextReset <= 0 {
		t.Fatal("expected next reset to be initialized")
	}
}

func TestQuotaDepletionAndIncreaseAutoRestore(t *testing.T) {
	setupClientTestDB(t)
	svc := &ClientService{}

	client := baseClient("haichao")
	client.Up = 100 * gib
	client.Down = 100 * gib
	client = saveClient(t, svc, "new", client)

	if _, err := svc.DepleteClients(); err != nil {
		t.Fatalf("deplete clients: %v", err)
	}
	if err := database.GetDB().First(&client, client.Id).Error; err != nil {
		t.Fatalf("reload depleted client: %v", err)
	}
	if client.Enable {
		t.Fatal("expected exhausted quota client to be disabled")
	}
	if client.DisableReason != DisableReasonQuota {
		t.Fatalf("expected quota disable reason, got %q", client.DisableReason)
	}

	client.Volume = 250 * gib
	client.Enable = false
	client = saveClient(t, svc, "edit", client)

	if !client.Enable {
		t.Fatal("expected quota increase to re-enable quota-disabled client")
	}
	if client.DisableReason != "" {
		t.Fatalf("expected disable reason to be cleared, got %q", client.DisableReason)
	}
	if client.Up+client.Down != 200*gib {
		t.Fatal("expected usage to be preserved after quota increase")
	}
}

func TestManualDisableIsNotAutoRestoredByQuotaIncrease(t *testing.T) {
	setupClientTestDB(t)
	svc := &ClientService{}

	client := baseClient("wuyuxue")
	client.Enable = false
	client.Up = 50 * gib
	client = saveClient(t, svc, "new", client)

	client.Volume = 250 * gib
	client.Enable = false
	client = saveClient(t, svc, "edit", client)

	if client.Enable {
		t.Fatal("expected manually disabled client to stay disabled")
	}
	if client.DisableReason != "" {
		t.Fatalf("expected manual disable to keep empty reason, got %q", client.DisableReason)
	}
}

func TestQuotaDecreaseDisablesImmediately(t *testing.T) {
	setupClientTestDB(t)
	svc := &ClientService{}

	client := baseClient("lower")
	client.Volume = 250 * gib
	client.Up = 200 * gib
	client = saveClient(t, svc, "new", client)

	client.Volume = 150 * gib
	client.Enable = true
	client = saveClient(t, svc, "edit", client)

	if client.Enable {
		t.Fatal("expected quota decrease below usage to disable the client")
	}
	if client.DisableReason != DisableReasonQuota {
		t.Fatalf("expected quota disable reason, got %q", client.DisableReason)
	}
}
