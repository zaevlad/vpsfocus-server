package store

import (
	"testing"
	"time"

	"agent/internal/checks"
	"agent/internal/probe"
)

func result(projectID, url string, ok bool) probe.Result {
	return probe.Result{
		ProjectID: projectID,
		Name:      url,
		URL:       url,
		Kind:      probe.KindPage,
		OK:        ok,
		CheckedAt: time.Now().UTC().Truncate(time.Second),
	}
}

// named — вторая проверка того же адреса: свой вид, своё имя.
func named(projectID, url, name string, kind probe.Kind, ok bool) probe.Result {
	item := result(projectID, url, ok)
	item.Name = name
	item.Kind = kind
	return item
}

func keys(items ...probe.Result) map[ProbeKey]bool {
	alive := map[ProbeKey]bool{}
	for _, item := range items {
		alive[KeyOfProbe(item)] = true
	}
	return alive
}

// Хранится последний исход, а не история: круг ходит каждые несколько
// минут, и вторая запись по тому же адресу обязана заменить первую.
func TestProbeResultIsReplaced(t *testing.T) {
	db := openTestStore(t)
	alive := keys(result("p1", "https://a.test/", true))

	if err := db.SaveProbeResult(t.Context(), result("p1", "https://a.test/", false)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProbeResult(t.Context(), result("p1", "https://a.test/", true)); err != nil {
		t.Fatal(err)
	}

	results, err := db.ProbeResults(t.Context(), alive)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("исходов %d, ждали один", len(results))
	}
	if !results[0].OK {
		t.Fatal("остался прошлый исход: история вместо последнего результата")
	}
}

// Адрес убрали из конфига — показывать вчерашнюю его беду больше незачем.
func TestForgottenProbeDisappears(t *testing.T) {
	db := openTestStore(t)

	for _, url := range []string{"https://a.test/1", "https://a.test/2"} {
		if err := db.SaveProbeResult(t.Context(), result("p1", url, false)); err != nil {
			t.Fatal(err)
		}
	}

	alive := keys(result("p1", "https://a.test/1", false))
	if err := db.ForgetProbeResults(t.Context(), alive); err != nil {
		t.Fatal(err)
	}

	// Читаем без фильтра: проверяем, что строка удалена, а не спрятана.
	all := keys(
		result("p1", "https://a.test/1", false),
		result("p1", "https://a.test/2", false),
	)
	results, err := db.ProbeResults(t.Context(), all)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("осталось %d, ждали один", len(results))
	}
	if results[0].URL != "https://a.test/1" {
		t.Fatalf("остался %q", results[0].URL)
	}
}

// Один адрес у двух проектов — две разные строки: ключ парой, а не склейкой.
func TestSameURLInTwoProjectsStaysApart(t *testing.T) {
	db := openTestStore(t)

	if err := db.SaveProbeResult(t.Context(), result("p1", "https://a.test/", true)); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProbeResult(t.Context(), result("p2", "https://a.test/", false)); err != nil {
		t.Fatal(err)
	}

	results, err := db.ProbeResults(t.Context(), keys(
		result("p1", "https://a.test/", true),
		result("p2", "https://a.test/", false),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("исходов %d, ждали два", len(results))
	}
}

// Две проверки одного адреса — осмысленная пара: «главная отвечает 200» и
// «на главной есть слово Каталог». Пока ключом были проект и адрес, они
// затирали исход друг друга каждый круг, а сравнение свежего результата с
// чужим прошлым объявляло перелом на ровном месте: письмо о беде, которой
// нет, и молчание о настоящей.
func TestTwoProfilesOnOneURLKeepTheirOwnOutcome(t *testing.T) {
	db := openTestStore(t)

	page := named("p1", "https://a.test/", "главная отвечает", probe.KindPage, true)
	text := named("p1", "https://a.test/", "есть слово Каталог", probe.KindText, false)

	if err := db.SaveProbeResult(t.Context(), page); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProbeResult(t.Context(), text); err != nil {
		t.Fatal(err)
	}

	results, err := db.ProbeResults(t.Context(), keys(page, text))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("исходов %d, ждали два: проверки затёрли друг друга", len(results))
	}
	for _, item := range results {
		switch item.Name {
		case "главная отвечает":
			if !item.OK {
				t.Fatal("исход страницы подменён исходом соседней проверки")
			}
		case "есть слово Каталог":
			if item.OK {
				t.Fatal("исход текстовой проверки подменён исходом соседней")
			}
		default:
			t.Fatalf("неизвестная проверка %q", item.Name)
		}
	}
}

// Две проверки одного вида на одном адресе различаются именем — тем самым,
// которое человек видит на экране и читает в письме.
func TestTwoTextChecksOnOneURLStayApart(t *testing.T) {
	db := openTestStore(t)

	catalog := named("p1", "https://a.test/", "Каталог", probe.KindText, true)
	cart := named("p1", "https://a.test/", "Корзина", probe.KindText, false)

	if err := db.SaveProbeResult(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProbeResult(t.Context(), cart); err != nil {
		t.Fatal(err)
	}

	results, err := db.ProbeResults(t.Context(), keys(catalog, cart))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("исходов %d, ждали два", len(results))
	}
}

// Безымянная проверка называется своим адресом — так же, как её называет
// разбор конфига. Считай хранилище иначе, живой профиль не совпал бы с
// собственным исходом, и уборка стирала бы его каждый круг.
func TestNamelessProfileMatchesItsOutcome(t *testing.T) {
	if KeyOfProfile(probe.Profile{
		ProjectID: "p1",
		URL:       "https://a.test/",
		Kind:      probe.KindPage,
	}) != KeyOfProbe(result("p1", "https://a.test/", true)) {
		t.Fatal("личность профиля и личность его исхода разошлись")
	}
}

// Снятая проверка не оставляет тревогу горящей: вернувшись и снова
// сломавшись, она промолчала бы — dispatch счёл бы беду уже известной. То же
// правило, что у ForgetChecks: сайт удалили — его сроки больше не наши.
func TestForgottenProbeClearsItsAlert(t *testing.T) {
	db := openTestStore(t)

	item := result("p1", "https://a.test/1", false)
	target := ProbeAlertTarget(KeyOfProbe(item))
	if err := db.SaveProbeResult(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAlertState(t.Context(), checks.KindProbe, target,
		AlertState{Firing: true, LastSent: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if err := db.ForgetProbeResults(t.Context(), map[ProbeKey]bool{}); err != nil {
		t.Fatal(err)
	}

	state, err := db.AlertState(t.Context(), checks.KindProbe, target)
	if err != nil {
		t.Fatal(err)
	}
	if state.Firing {
		t.Fatal("тревога снятой проверки осталась гореть")
	}
}

// Тревога у каждой проверки своя, даже когда адрес один.
//
// Пока целью была строка адреса, две проверки делили одну тревогу: беда
// второй молчала сутки, пока горела первая, а отбой первой гасил состояние
// второй. Тест держит обе половины сразу — и что цели разные, и что снятие
// одной проверки не трогает тревогу соседней.
func TestTwoChecksOnOneURLKeepTheirOwnAlert(t *testing.T) {
	db := openTestStore(t)

	page := named("p1", "https://a.test/", "главная отвечает", probe.KindPage, false)
	text := named("p1", "https://a.test/", "есть слово Каталог", probe.KindText, false)

	pageTarget := ProbeAlertTarget(KeyOfProbe(page))
	textTarget := ProbeAlertTarget(KeyOfProbe(text))
	if pageTarget == textTarget {
		t.Fatal("две проверки одного адреса делят одну тревогу")
	}

	for _, item := range []probe.Result{page, text} {
		if err := db.SaveProbeResult(t.Context(), item); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveAlertState(t.Context(), checks.KindProbe,
			ProbeAlertTarget(KeyOfProbe(item)),
			AlertState{Firing: true, LastSent: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}

	// Снимаем текстовую проверку, страничная остаётся.
	if err := db.ForgetProbeResults(t.Context(), keys(page)); err != nil {
		t.Fatal(err)
	}

	live, err := db.AlertState(t.Context(), checks.KindProbe, pageTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !live.Firing {
		t.Fatal("тревога живой проверки погашена снятием соседней")
	}

	gone, err := db.AlertState(t.Context(), checks.KindProbe, textTarget)
	if err != nil {
		t.Fatal(err)
	}
	if gone.Firing {
		t.Fatal("тревога снятой проверки осталась гореть")
	}
}

// Строка тревоги от прежнего выпуска, где целью был один адрес, уходит с
// первой же уборкой: чистим по списку живых, а не по списку снятых.
func TestAlertFromTheOldFormatIsSweptAway(t *testing.T) {
	db := openTestStore(t)

	item := result("p1", "https://a.test/", false)
	if err := db.SaveProbeResult(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	// Так тревогу записывал агент до того, как у проверки появилась своя
	// личность: целью шёл голый адрес.
	if err := db.SaveAlertState(t.Context(), checks.KindProbe, "https://a.test/",
		AlertState{Firing: true, LastSent: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if err := db.ForgetProbeResults(t.Context(), keys(item)); err != nil {
		t.Fatal(err)
	}

	stale, err := db.AlertState(t.Context(), checks.KindProbe, "https://a.test/")
	if err != nil {
		t.Fatal(err)
	}
	if stale.Firing {
		t.Fatal("тревога прежнего формата пережила уборку")
	}
}
