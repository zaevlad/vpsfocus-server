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
