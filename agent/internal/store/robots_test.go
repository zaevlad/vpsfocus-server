package store

import (
	"context"
	"testing"
	"time"

	"agent/internal/logs"
)

// Счётчики роботов складываются между проходами, сводятся за период по
// сайту и имени и уходят вместе с ротацией логов.
func TestRobotCountsAddUpAndRotate(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	today := now.Unix() - now.Unix()%86_400
	old := today - 40*86_400

	pass := []logs.RobotCount{
		{Day: today, Host: "shop.example", Name: "GPTBot", Count: 2, LastAt: now.Add(-time.Hour)},
		{Day: old, Host: "shop.example", Name: "GPTBot", Count: 5, LastAt: now.AddDate(0, 0, -40)},
	}
	if err := db.AddRobotCounts(ctx, pass); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRobotCounts(ctx, []logs.RobotCount{
		{Day: today, Host: "shop.example", Name: "GPTBot", Count: 3, LastAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	recent, err := db.RobotSummary(ctx, now.AddDate(0, 0, -14))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Count != 5 || recent[0].LastAt.Unix() != now.Unix() {
		t.Fatalf("за две недели: %+v", recent)
	}

	if _, err := db.RotateLogEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	all, err := db.RobotSummary(ctx, now.AddDate(0, 0, -100))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Count != 5 {
		t.Fatalf("старые счётчики пережили ротацию: %+v", all)
	}
}

// Отказы роботам складываются между проходами, читаются по суткам и уходят
// с ротацией вместе со счётчиками визитов.
func TestRobotRefusalsAddUpAndRotate(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	today := now.Unix() - now.Unix()%86_400
	old := today - 40*86_400

	pass := []logs.RobotRefusal{
		{Day: today, Host: "shop.example", Name: "GPTBot", Status: 403, Count: 2},
		{Day: today, Host: "shop.example", Name: "GPTBot", Status: 403, RobotsTxt: true, Count: 1},
		{Day: old, Host: "shop.example", Name: "GPTBot", Status: 429, Count: 7},
	}
	for range 2 {
		if err := db.AddRobotRefusals(ctx, pass); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := db.RobotRefusals(ctx, now.AddDate(0, 0, -14))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("за две недели: %+v", recent)
	}
	for _, item := range recent {
		want := 4
		if item.RobotsTxt {
			want = 2
		}
		if item.Count != want {
			t.Errorf("не сложилось: %+v", item)
		}
	}

	if _, err := db.RotateLogEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	all, err := db.RobotRefusals(ctx, now.AddDate(0, 0, -100))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("старые отказы пережили ротацию: %+v", all)
	}

	days, err := db.RobotDays(ctx, now.AddDate(0, 0, -14))
	if err != nil || len(days) != 0 {
		t.Fatalf("суточные счётчики: %+v %v", days, err)
	}
}
