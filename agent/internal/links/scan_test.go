package links

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Сборка отчёта — то место, где сходятся обход и проверка.
//
// Пока она жила в вызывающем, теряла оба своих поля: «Страниц» показывало
// ноль всегда, а длительностью круга называлась длительность одного lychee,
// без обхода. Тесты здесь именно про сборку, а не про разбор вывода lychee.

func TestScanCountsCrawledPages(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(`<a href="/one">раз</a><a href="/two">два</a>`))
		default:
			_, _ = w.Write([]byte("<p>страница</p>"))
		}
	})
	stubRunner(t, fixtureOutput)

	report, err := Scan(testContext(t), HTTPClient(0), server.URL+"/",
		Limits{MaxDepth: 2, MaxPages: 10}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Три страницы: главная и две с неё.
	if report.Pages != 3 {
		t.Errorf("страниц в отчёте %d, ожидали 3", report.Pages)
	}
	// Данные lychee при этом на месте: сборка их не затирает.
	if report.TotalLinks != 6 || report.Broken != 2 {
		t.Errorf("данные проверки потерялись: ссылок %d, битых %d",
			report.TotalLinks, report.Broken)
	}
	if report.Domain != hostOf(server.URL) {
		t.Errorf("домен в отчёте %q", report.Domain)
	}
	if report.CheckedAt == "" {
		t.Error("время начала круга не проставлено")
	}
}

// Длительность — весь круг, а не одна проверка.
//
// Фикстура lychee сообщает пять секунд; настоящий круг здесь занимает доли
// секунды. Если бы в отчёт уезжало число от lychee, оно бы и осталось.
func TestScanMeasuresWholeRound(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("<p>страница</p>"))
	})
	stubRunner(t, fixtureOutput)

	report, err := Scan(testContext(t), HTTPClient(0), server.URL+"/",
		Limits{MaxDepth: 0, MaxPages: 10}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if report.DurationSeconds >= 5 {
		t.Errorf("длительность круга %d с — это число из вывода lychee, а не наш замер",
			report.DurationSeconds)
	}
}

// Неудачный обход отдаёт отчёт, а не пустоту: экрану нужно «не получилось и
// почему», а вызывающему — куда положить код ошибки.
func TestScanReturnsReportOnFailure(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "не сегодня", http.StatusInternalServerError)
	})

	report, err := Scan(testContext(t), HTTPClient(0), server.URL+"/",
		Limits{MaxDepth: 1, MaxPages: 10}, Options{})

	if !errors.Is(err, ErrRobotsUnavailable) {
		t.Fatalf("ошибка %v, ожидали ErrRobotsUnavailable", err)
	}
	if report.Domain != hostOf(server.URL) || report.CheckedAt == "" {
		t.Errorf("отчёт о неудаче пуст: %+v", report)
	}
	if report.Failures == nil {
		t.Error("список находок nil: экран ждёт пустой массив, а не отсутствие поля")
	}
}

// Проверка не состоялась, а обход состоялся — число страниц в отчёте
// остаётся: оно уже известно, и терять его незачем.
func TestScanKeepsPagesWhenCheckFails(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(`<a href="/one">раз</a>`))
		default:
			_, _ = w.Write([]byte("<p>страница</p>"))
		}
	})

	failure := errors.New("lychee не запустился")
	runLycheeFunc = func(context.Context, Options, []string) (*lycheeOutput, error) {
		return nil, failure
	}
	t.Cleanup(func() { runLycheeFunc = runLychee })

	report, err := Scan(testContext(t), HTTPClient(0), server.URL+"/",
		Limits{MaxDepth: 2, MaxPages: 10}, Options{})

	if !errors.Is(err, failure) {
		t.Fatalf("ошибка %v, ожидали ошибку запуска lychee", err)
	}
	if report.Pages != 2 {
		t.Errorf("страниц в отчёте %d, ожидали 2: обход-то состоялся", report.Pages)
	}
}

// Круг, убитый нашим же таймаутом, отличим от круга, который сломался.
//
// Ошибка от lychee заворачивалась через %v, и errors.Is не срабатывал
// никогда: перевод про «не уложились во время» не видел никто.
func TestRunLycheeWrapsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	// Бинарником берётся сам тест: он заведомо существует, а до запуска
	// дело не дойдёт — истёкший контекст отсекает его раньше. С
	// несуществующим именем сработал бы поиск по PATH, и до причины,
	// которую проверяет тест, мы бы не добрались.
	_, err := runLychee(ctx, Options{Binary: os.Args[0], TimeoutSeconds: 1},
		[]string{"https://example.com/"})
	if err == nil {
		t.Fatal("запуск с истёкшим контекстом обязан вернуть ошибку")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("причина не пролезла наружу: %v", err)
	}
}

// hostOf — хост тестового сервера: в отчёт уезжает он, а не вся ссылка.
func hostOf(rawURL string) string {
	return strings.TrimPrefix(rawURL, "http://")
}
