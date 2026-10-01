package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Край стека проверяется у своего Caddy, а не там, куда смотрит DNS (аудит
// 2026-09-22): соединение идёт по адресу `dial`, а имя в сертификате
// сверяется с доменом трекинга. Имя здесь нарочно не резолвится вовсе —
// значит, до сертификата дошли только через `dial`.
func TestCheckTLSAtDialsTheGivenAddress(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	status := CheckTLSAt(context.Background(), "example.com", 2096, server.Listener.Addr().String())

	if status.Error == ErrTLSUnreachable || status.Error == ErrTLSHandshakeBad {
		t.Fatalf("до сервера не дошли: %q", status.Error)
	}
	if status.ExpiresAt == nil {
		t.Fatal("срок сертификата не прочитан")
	}
	if status.Target() != "example.com:2096" {
		t.Errorf("цель %q, ожидалась example.com:2096", status.Target())
	}
}
