package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Сборщик ходит в сеть только за файлами из перечня в sources.go и только
// по https, с нашим User-agent и пределом размера: чужой сервер, отдавший
// гигабайт или перенаправивший на http, роняет сборку, а не машину.
const (
	userAgent = "vpsfocus-bots-build (+https://vpsfocus.xyz/server-code)"
	maxBody   = 8 << 20
)

var client = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("перенаправление не на https: %s", req.URL)
		}
		if len(via) >= 5 {
			return errors.New("слишком много перенаправлений")
		}
		return nil
	},
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%s: только https", url)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: ответ %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s: больше %d байт", url, maxBody)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%s: пустой ответ", url)
	}
	return body, nil
}
