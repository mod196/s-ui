package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
)

func setTrafficPoolNow(t *testing.T, now time.Time) {
	t.Helper()
	old := trafficPoolNow
	trafficPoolNow = func() time.Time {
		return now
	}
	t.Cleanup(func() {
		trafficPoolNow = old
	})
}

func TestTrafficPoolAnchorInitializesOnce(t *testing.T) {
	setupClientTestDB(t)
	firstStart := time.Unix(1800000000, 123).UTC()
	setTrafficPoolNow(t, firstStart)

	cfg, err := (&SettingService{}).GetTrafficPoolConfig()
	if err != nil {
		t.Fatalf("get traffic pool config: %v", err)
	}
	if cfg.AnchorAt != firstStart.Unix() {
		t.Fatalf("expected anchor %d, got %d", firstStart.Unix(), cfg.AnchorAt)
	}

	trafficPoolNow = func() time.Time {
		return firstStart.Add(24 * time.Hour)
	}
	cfg, err = (&SettingService{}).GetTrafficPoolConfig()
	if err != nil {
		t.Fatalf("get traffic pool config again: %v", err)
	}
	if cfg.AnchorAt != firstStart.Unix() {
		t.Fatalf("expected persisted anchor %d, got %d", firstStart.Unix(), cfg.AnchorAt)
	}
}

func TestTrafficPoolWindowRollsEveryThirtyDays(t *testing.T) {
	anchor := int64(1779494400)
	cycle := int64(defaultTrafficPoolCycleDays) * 86400

	start, end := trafficPoolWindow(anchor, defaultTrafficPoolCycleDays, time.Unix(anchor+cycle-1, 0))
	if start != anchor || end != anchor+cycle {
		t.Fatalf("expected first window [%d,%d), got [%d,%d)", anchor, anchor+cycle, start, end)
	}

	start, end = trafficPoolWindow(anchor, defaultTrafficPoolCycleDays, time.Unix(anchor+cycle, 0))
	if start != anchor+cycle || end != anchor+2*cycle {
		t.Fatalf("expected second window [%d,%d), got [%d,%d)", anchor+cycle, anchor+2*cycle, start, end)
	}
}

func TestTrafficPoolUsesConfiguredAnchorAndUserStatsOnly(t *testing.T) {
	setupClientTestDB(t)
	anchor := int64(1779494400)
	cycle := int64(defaultTrafficPoolCycleDays) * 86400
	setTrafficPoolNow(t, time.Unix(anchor+3600, 0))

	settings := &SettingService{}
	if err := settings.saveSetting("trafficPoolAnchorAt", strconv.FormatInt(anchor, 10)); err != nil {
		t.Fatalf("save anchor: %v", err)
	}

	stats := []model.Stats{
		{DateTime: anchor - 1, Resource: "user", Tag: "leo", Direction: true, Traffic: 999 * gib},
		{DateTime: anchor + 10, Resource: "user", Tag: "leo", Direction: true, Traffic: 80 * gib},
		{DateTime: anchor + 20, Resource: "user", Tag: "leo", Direction: false, Traffic: 46 * gib},
		{DateTime: anchor + 30, Resource: "user", Tag: "haichao", Direction: false, Traffic: 42 * gib},
		{DateTime: anchor + 40, Resource: "inbound", Tag: "mixed-in", Direction: false, Traffic: 700 * gib},
		{DateTime: anchor + cycle, Resource: "user", Tag: "next-cycle", Direction: false, Traffic: 300 * gib},
	}
	if err := database.GetDB().Create(&stats).Error; err != nil {
		t.Fatalf("create stats: %v", err)
	}

	pool, err := (&StatsService{}).GetTrafficPool()
	if err != nil {
		t.Fatalf("get traffic pool: %v", err)
	}

	if pool.StartedAt != anchor || pool.EndedAt != anchor+cycle || pool.NextResetAt != pool.EndedAt {
		t.Fatalf("unexpected pool window: start=%d end=%d next=%d", pool.StartedAt, pool.EndedAt, pool.NextResetAt)
	}
	if pool.Used != 168*gib {
		t.Fatalf("expected used 168 GiB, got %d", pool.Used/gib)
	}
	if len(pool.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(pool.Users))
	}
	if pool.Users[0].Name != "leo" || pool.Users[0].Total != 126*gib {
		t.Fatalf("expected leo to be top user with 126 GiB, got %+v", pool.Users[0])
	}
	if pool.Users[1].Name != "haichao" || pool.Users[1].Total != 42*gib {
		t.Fatalf("expected haichao with 42 GiB, got %+v", pool.Users[1])
	}
	if pool.CycleDays != defaultTrafficPoolCycleDays || pool.Source != defaultTrafficPoolSource {
		t.Fatalf("unexpected pool metadata: cycle=%d source=%q", pool.CycleDays, pool.Source)
	}
}
