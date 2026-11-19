package logs

import (
	"path"
	"strings"
	"time"

	"agent/internal/bots"
)

// Что читали ИИ по просьбе людей (трек AI, спринт 274, Р11, Р37, Р38).
//
// ChatGPT-User, Claude-User и похожие приходят, когда человек спросил ИИ о
// чём-то и тот пошёл на сайт. Какие страницы он открыл и нашёл ли их — самое
// полезное для владельца сайта. Хранится «сутки, сайт, имя, путь, код» и
// число; путь — как у ошибок (`NormalizePath`: без строки запроса, номера —
// `{n}`, идентификаторы — `{id}`, Р61). Пути людей не пишутся никогда —
// только роботов этой роли.

// PagesPerRobotDay — потолок путей на сутки, сайт и робота (Р11): остальное —
// одной строкой OtherPath.
const PagesPerRobotDay = 50

// OtherPath — «остальные страницы» сверх потолка.
const OtherPath = "@other"

// RobotPage — сколько раз за сутки робот ИИ открыл страницу с этим кодом.
type RobotPage struct {
	Day    int64
	Host   string
	Name   string
	Path   string
	Status int
	Count  int
}

type pageKey struct {
	day    int64
	host   string
	name   string
	path   string
	status int
}

// Расширения статики (Р11): картинки, стили, скрипты, шрифты, видео —
// страница их подтягивает сама, это не то, что ИИ «читал». Документы
// (.pdf и т. п.) остаются: их ИИ открывает как страницы.
var staticExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
	".svg": true, ".ico": true, ".bmp": true,
	".css": true, ".js": true, ".mjs": true, ".map": true, ".json": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
	".mp3": true, ".mp4": true, ".webm": true, ".mov": true, ".avi": true,
}

// pageVisit — считается ли строка страницей, которую читал ИИ (Р37):
// только `GET`, не `/robots.txt`, не статика, не перебор сканера.
func pageVisit(event Event) bool {
	if event.Method != "GET" || event.Path == "" || event.Path == "/robots.txt" {
		return false
	}
	if bots.Scanner(event.Path) {
		return false
	}
	return !staticExtensions[strings.ToLower(path.Ext(event.Path))]
}

// addPage учитывает страницу робота по просьбе человека. Самозванцев не
// пишем (Р11): «что ИИ читал» про них было бы неправдой.
func (r *Robots) addPage(event Event, outcome string) {
	if outcome == bots.Unverified || !pageVisit(event) {
		return
	}
	if role, _ := bots.KindOf(event.Robot); role != bots.AIUser {
		return
	}
	day := dayOf(event.At)
	key := pageKey{day, event.Host, event.Robot, event.Path, event.Status}
	if item, ok := r.pages[key]; ok {
		item.Count++
		return
	}
	paths := [3]string{time.Unix(day, 0).UTC().Format("2006-01-02"), event.Host, event.Robot}
	if r.pagePaths[paths] >= PagesPerRobotDay {
		key.path = OtherPath
		key.status = 0
		if item, ok := r.pages[key]; ok {
			item.Count++
			return
		}
	} else {
		r.pagePaths[paths]++
	}
	r.pages[key] = &RobotPage{Day: day, Host: key.host, Name: key.name, Path: key.path, Status: key.status, Count: 1}
}

// Pages — страницы роботов ИИ за проход, для таблицы robot_pages.
func (r *Robots) Pages() []RobotPage {
	out := make([]RobotPage, 0, len(r.pages))
	for _, item := range r.pages {
		out = append(out, *item)
	}
	return out
}

// Исход страницы для экрана (Р37): нашёл, перенаправлен, не нашёл, ошибка
// сервера. 3xx — не «не нашёл».
const (
	PageOpened     = "opened"
	PageRedirected = "redirected"
	PageNotFound   = "notFound"
	PageFailed     = "failed"
)

// PageOutcome — исход по коду ответа.
func PageOutcome(status int) string {
	switch {
	case status >= 500:
		return PageFailed
	case status >= 400:
		return PageNotFound
	case status >= 300:
		return PageRedirected
	default:
		return PageOpened
	}
}
