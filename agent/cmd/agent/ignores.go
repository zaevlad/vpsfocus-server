package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"agent/internal/config"
	"agent/internal/ignore"
	"agent/internal/seo"
	"agent/internal/vitals"
)

// «Не учитывать» (трек AG): записи, которые ставит и снимает приложение.
//
// Список хранится здесь, у агента (Р1), а не в приложении: он общий на
// команду, а отчёт, тревоги и показатели считает агент — храни список в
// приложении, письма продолжали бы считать эти ссылки битыми.

// IgnoreItem — запись и сколько пунктов она покрывает в последних данных
// (Р9): ноль — «сейчас не встречается», запись при этом не удаляется.
type IgnoreItem struct {
	ignore.Entry
	Covers int `json:"covers"`
}

// IgnoresState — ответ `GET /ignores`.
type IgnoresState struct {
	Ignores []IgnoreItem `json:"ignores"`
	// Предел записей на сервер (Р15): экран называет его в отказе.
	Limit int `json:"limit"`
}

// IgnoreAdded — ответ `POST /ignores`.
type IgnoreAdded struct {
	Entry IgnoreItem `json:"entry"`
	// Такая запись уже была (Р29): вторая копия приложения успела раньше.
	Existed bool `json:"existed"`
}

// IgnoreRemoved — ответ `DELETE /ignores`.
type IgnoreRemoved struct {
	// false — записи уже не было, и это не ошибка.
	Removed bool `json:"removed"`
}

// ignoreList — записи сервера для чтения отчётов.
//
// Не прочиталась база — отчёты отдаются без вычитания, с записью в лог.
// Это сторона «показать больше, чем просили», а не «спрятать беду»: экран
// увидит лишнюю находку, а не потеряет настоящую.
func (a *Agent) ignoreList(ctx context.Context) ignore.List {
	list, err := a.db.Ignores(ctx)
	if err != nil {
		log.Printf("не учитывать: %v", err)
		return nil
	}
	return list
}

// forgetIgnores убирает записи сайтов, которых больше нет (Р30), — только
// по прочитанному без ошибки и непустому списку сайтов (Р37).
func (a *Agent) forgetIgnores(ctx context.Context, sites []config.Site, readErr error) {
	if readErr != nil || len(sites) == 0 {
		return
	}
	alive := make(map[string]bool, len(sites))
	for _, site := range sites {
		alive[site.Domain] = true
	}
	if err := a.db.ForgetIgnores(ctx, alive); err != nil {
		log.Printf("не учитывать: чистка по списку сайтов: %v", err)
	}
}

// Ignores отдаёт записи с тем, сколько каждая покрывает сейчас.
func (a *Agent) Ignores() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sites, err := a.cfg.Sites()
	a.forgetIgnores(ctx, sites, err)

	list, err := a.db.Ignores(ctx)
	if err != nil {
		return nil, err
	}
	coverage, err := a.coverage(ctx, list)
	if err != nil {
		return nil, err
	}
	items := make([]IgnoreItem, 0, len(list))
	for index, entry := range list {
		items = append(items, IgnoreItem{Entry: entry, Covers: coverage[index]})
	}
	return IgnoresState{Ignores: items, Limit: ignore.Limit}, nil
}

// AddIgnore ставит запись. Сайт должен быть в списке сайтов мониторинга.
func (a *Agent) AddIgnore(entry ignore.Entry) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	entry, err := a.checkedIgnore(entry)
	if err != nil {
		return nil, err
	}
	stored, existed, err := a.db.AddIgnore(ctx, entry, time.Now())
	if err != nil {
		return nil, err
	}
	coverage, err := a.coverage(ctx, ignore.List{stored})
	if err != nil {
		return nil, err
	}
	return IgnoreAdded{Entry: IgnoreItem{Entry: stored, Covers: coverage[0]}, Existed: existed}, nil
}

// PreviewIgnore — та же запись без записи: сколько она покрыла бы сейчас.
func (a *Agent) PreviewIgnore(entry ignore.Entry) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	entry, err := a.checkedIgnore(entry)
	if err != nil {
		return nil, err
	}
	coverage, err := a.coverage(ctx, ignore.List{entry})
	if err != nil {
		return nil, err
	}
	return IgnoreAdded{Entry: IgnoreItem{Entry: entry, Covers: coverage[0]}}, nil
}

// checkedIgnore приводит запись и проверяет, что сайт — в списке сайтов
// мониторинга: запись чужого домена никто бы не увидел.
func (a *Agent) checkedIgnore(entry ignore.Entry) (ignore.Entry, error) {
	entry, err := ignore.Normalize(entry)
	if err != nil {
		return ignore.Entry{}, err
	}
	sites, err := a.cfg.Sites()
	if err != nil {
		return ignore.Entry{}, err
	}
	for _, site := range sites {
		if site.Domain == entry.Domain {
			return entry, nil
		}
	}
	return ignore.Entry{}, fmt.Errorf("%w: %s", ignore.ErrUnknownSite, entry.Domain)
}

// RemoveIgnore снимает запись — «учитывать снова».
func (a *Agent) RemoveIgnore(entry ignore.Entry) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ключ приводится тем же способом, что при записи: иначе «/ru/» с
	// экрана не нашёл бы записанное «/ru».
	entry, err := ignore.Normalize(entry)
	if err != nil {
		return nil, err
	}
	removed, err := a.db.RemoveIgnore(ctx, entry.Key())
	if err != nil {
		return nil, err
	}
	return IgnoreRemoved{Removed: removed}, nil
}

// coverage — сколько пунктов последних данных покрывает каждая запись: у
// ссылки — битых ссылок, у SEO-проверки — страниц (у проверки о сайте
// целиком — 1, если она сейчас не пройдена).
//
// У совета Lighthouse — число страниц, где он есть в последнем замере
// хотя бы одной стратегии (Р23: стратегии — одной записью).
func (a *Agent) coverage(ctx context.Context, list ignore.List) ([]int, error) {
	out := make([]int, len(list))
	if len(list) == 0 {
		return out, nil
	}

	domains := map[string]bool{}
	for _, entry := range list {
		domains[entry.Domain] = true
	}

	// Сырые отчёты, без вычитания: вопрос как раз в том, что вычитается.
	reports, err := a.db.LinkReports(ctx, domains, nil)
	if err != nil {
		return nil, err
	}
	scans, err := a.db.SeoScans(ctx, domains, nil)
	if err != nil {
		return nil, err
	}
	advice, err := a.rawAdvice(ctx, list)
	if err != nil {
		return nil, err
	}
	seoPages := map[string]map[string][]string{}
	for _, entry := range list {
		if entry.Kind != ignore.KindSeo || seo.SiteWide(entry.What) {
			continue
		}
		if _, ok := seoPages[entry.Domain]; ok {
			continue
		}
		current, _, err := a.db.SeoScanCodes(ctx, entry.Domain)
		if err != nil {
			return nil, err
		}
		seoPages[entry.Domain] = current.Pages
	}

	for index, entry := range list {
		switch entry.Kind {
		case ignore.KindLink:
			for _, report := range reports {
				if report.Domain != entry.Domain {
					continue
				}
				_, ignored := ignore.SplitFailures(report.Failures, ignore.List{entry})
				out[index] = len(ignored)
			}
		case ignore.KindAdvice:
			pages := map[string]bool{}
			for _, report := range advice {
				if report.Domain != entry.Domain || report.Lab == nil {
					continue
				}
				page := report.URL
				if page == "" {
					page = report.Path
				}
				for _, item := range report.Lab.Advice {
					if entry.CoversAdvice(item.ID, page) {
						pages[ignore.PagePath(page)] = true
					}
				}
			}
			out[index] = len(pages)
		case ignore.KindSeo:
			if seo.SiteWide(entry.What) {
				for _, scan := range scans {
					if scan.Domain == entry.Domain && siteWideFailing(scan.Stats, entry.What) {
						out[index] = 1
					}
				}
				continue
			}
			for page, codes := range seoPages[entry.Domain] {
				for _, code := range codes {
					if entry.CoversSeo(code, page) {
						out[index]++
						break
					}
				}
			}
		}
	}
	return out, nil
}

// siteWideFailing — не пройдена ли проверка о сайте целиком в этом обходе.
func siteWideFailing(stats seo.Stats, code string) bool {
	if stats.Site != nil {
		for _, issue := range stats.Site.Issues {
			if issue.Code == code {
				return true
			}
		}
	}
	if stats.Canonical != nil {
		for _, issue := range stats.Canonical.Issues {
			if issue.Code == code {
				return true
			}
		}
	}
	return false
}

// rawAdvice — последние замеры скорости сайтов с записями о советах, все
// сохранённые советы, без вычитания.
func (a *Agent) rawAdvice(ctx context.Context, list ignore.List) ([]vitals.Report, error) {
	wanted := map[string]bool{}
	for _, entry := range list {
		if entry.Kind == ignore.KindAdvice {
			wanted[entry.Domain] = true
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	sites, err := a.cfg.Sites()
	if err != nil {
		return nil, err
	}
	extra, err := a.cfg.Pages()
	if err != nil {
		extra = map[string][]string{}
	}
	var chosen []config.Site
	for _, site := range sites {
		if wanted[site.Domain] {
			chosen = append(chosen, site)
		}
	}
	return a.db.RawVitalsReports(ctx, a.vitalsAlive(chosen, extra))
}
