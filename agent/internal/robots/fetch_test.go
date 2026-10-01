package robots

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Сервер в миниатюре: отвечает по адресу тем, что ему велели.
type replies map[string]*http.Response

func serve(answers replies) Doer {
	return func(_ context.Context, target *url.URL) (*http.Response, error) {
		if response, ok := answers[target.String()]; ok {
			return response, nil
		}
		return text(http.StatusNotFound, ""), nil
	}
}

func text(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func redirect(status int, location string) *http.Response {
	response := text(status, "")
	response.Header.Set("Location", location)
	return response
}

func site(t *testing.T, address string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// Перенаправление на robots.txt — обычнейшая настройка сайта (http на
// https, www на апекс). Читать её как «файл не отдался» значит не обходить
// сайт вовсе: RFC 9309 §2.3.1.3 требует пройти хотя бы пять переходов.
func TestFetchFollowsRedirect(t *testing.T) {
	rules, verdict := Fetch(context.Background(), serve(replies{
		"http://example.test/robots.txt":  redirect(http.StatusMovedPermanently, "https://example.test/robots.txt"),
		"https://example.test/robots.txt": text(http.StatusOK, "User-agent: *\nDisallow: /secret/\n"),
	}), site(t, "http://example.test/"), testAgent)

	if verdict != Found {
		t.Fatalf("вердикт %v, ожидали Found", verdict)
	}
	if rules.Allowed("/secret/page") {
		t.Error("правила из файла за перенаправлением не применились")
	}
}

// Цепочка длиннее пяти переходов — это уже не «файл переехал», а петля.
func TestFetchStopsAfterTooManyRedirects(t *testing.T) {
	loop := replies{}
	for hop := 0; hop < 20; hop++ {
		loop[hopURL(hop)] = redirect(http.StatusFound, hopURL(hop+1))
	}

	if _, verdict := Fetch(context.Background(), serve(loop),
		site(t, "https://loop.test/"), testAgent); verdict != Unavailable {
		t.Fatalf("вердикт %v, ожидали Unavailable", verdict)
	}
}

func hopURL(hop int) string {
	if hop == 0 {
		return "https://loop.test/robots.txt"
	}
	return "https://loop.test/robots.txt?hop=" + string(rune('0'+hop%10)) + "-" + string(rune('a'+hop/10))
}

// Перенаправление на чужой домен — это не наш robots.txt. Применять правила
// соседнего сайта к сайту клиента значит ходить не туда, куда ходит
// поисковик.
func TestFetchRefusesForeignRedirect(t *testing.T) {
	_, verdict := Fetch(context.Background(), serve(replies{
		"https://example.test/robots.txt": redirect(http.StatusFound, "https://cdn.partner.net/robots.txt"),
		"https://cdn.partner.net/robots.txt": text(http.StatusOK,
			"User-agent: *\nDisallow: /\n"),
	}), site(t, "https://example.test/"), testAgent)

	if verdict != Unavailable {
		t.Fatalf("вердикт %v, ожидали Unavailable", verdict)
	}
}

// Отсутствие файла — разрешение ходить куда угодно, в отличие от файла,
// который не отдался.
func TestFetchTreatsMissingFileAsAllowAll(t *testing.T) {
	rules, verdict := Fetch(context.Background(), serve(replies{}),
		site(t, "https://example.test/"), testAgent)

	if verdict != Absent {
		t.Fatalf("вердикт %v, ожидали Absent", verdict)
	}
	if !rules.Allowed("/anything") {
		t.Error("отсутствие файла что-то запретило")
	}
}
