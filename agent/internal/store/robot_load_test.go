package store

import (
	"context"
	"testing"
	"time"

	"agent/internal/logs"
)

// Нагрузка и заходы в закрытое складываются между проходами, сводятся за
// период и уходят с ротацией логов (идея 7 трека AB).
func TestSiteTrafficAndViolationsAddUpAndRotate(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	today := now.Unix() - now.Unix()%86_400
	yesterday := today - 86_400
	old := today - 40*86_400

	for _, pass := range [][]logs.SiteTraffic{
		{{Day: today, Host: "a.example", Kind: logs.TrafficAll, Requests: 10, Bytes: 1000, Sized: 10}},
		{
			{Day: yesterday, Host: "a.example", Kind: logs.TrafficAll, Requests: 5, Bytes: 500, Sized: 5},
			{Day: today, Host: "a.example", Kind: "aiTraining", Requests: 4, Bytes: 800, Sized: 4},
			{Day: old, Host: "a.example", Kind: logs.TrafficAll, Requests: 99, Bytes: 99, Sized: 99},
		},
	} {
		if err := db.AddSiteTraffic(ctx, pass); err != nil {
			t.Fatal(err)
		}
	}
	traffic, err := db.SiteTraffic(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 2 || traffic[0].Kind != "aiTraining" || traffic[1].Requests != 15 || traffic[1].Bytes != 1500 {
		t.Fatalf("нагрузка за неделю: %+v", traffic)
	}

	if err := db.AddRobotViolations(ctx, []logs.RobotViolation{
		{Day: yesterday, Host: "a.example", Name: "GPTBot", Count: 2, Example: "/old"},
		{Day: today, Host: "a.example", Name: "GPTBot", Count: 1, Example: "/admin/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRobotViolations(ctx, []logs.RobotViolation{
		{Day: today, Host: "a.example", Name: "GPTBot", Count: 3, Example: "/later"},
	}); err != nil {
		t.Fatal(err)
	}
	violations, err := db.RobotViolations(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatal(err)
	}
	// Пример — из самых свежих суток и первый записанный в них.
	if len(violations) != 1 || violations[0].Count != 6 || violations[0].Example != "/admin/" {
		t.Fatalf("заходы в закрытое: %+v", violations)
	}

	if _, err := db.RotateLogEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	all, err := db.SiteTraffic(ctx, now.AddDate(0, 0, -100))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range all {
		if item.Requests >= 99 {
			t.Fatalf("старая нагрузка пережила ротацию: %+v", all)
		}
	}
}

// robots.txt из обхода: текст заменяется свежим, «файла нет» хранится
// пустым текстом, ушедший из списка сайт уходит.
func TestRobotsFilesKeepLatestAndForget(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)

	if err := db.SaveRobotsFile(ctx, "A.example", []byte("User-agent: *\nDisallow: /x"), at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRobotsFile(ctx, "a.example", []byte("User-agent: *\nDisallow: /y"), at); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRobotsFile(ctx, "b.example", nil, at); err != nil {
		t.Fatal(err)
	}
	files, err := db.RobotsFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(files["a.example"].Body) != "User-agent: *\nDisallow: /y" || !files["a.example"].FetchedAt.Equal(at) {
		t.Fatalf("свежий файл: %+v", files["a.example"])
	}
	if body := files["b.example"].Body; body == nil || len(body) != 0 {
		t.Fatalf("файла нет — пустой текст, а не «не знаем»: %#v", body)
	}

	if err := db.ForgetRobotsFiles(ctx, map[string]bool{"a.example": true}); err != nil {
		t.Fatal(err)
	}
	files, err = db.RobotsFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["b.example"]; ok || len(files) != 1 {
		t.Fatalf("ушедший сайт остался: %+v", files)
	}
}
