// Package proxy собирает HTTP-транспорт с прокси: http(s)://, socks5://, socks5h://.
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	xproxy "golang.org/x/net/proxy"
)

// Transport отдаёт транспорт под указанный прокси. Пустая строка — прямое
// соединение (nil, nil): вызывающий берёт обычный http.Client.
func Transport(raw string) (http.RoundTripper, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("прокси %q: нужен адрес вида socks5://127.0.0.1:1080", raw)
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("не удалось взять стандартный транспорт")
	}
	transport := base.Clone()

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)

	case "socks5", "socks5h":
		// x/net/proxy передаёт имя хоста прокси, а не резолвит его локально:
		// socks5 и socks5h работают одинаково.
		dial, err := socksDialer(u)
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		transport.DialContext = dial

	default:
		return nil, fmt.Errorf("прокси %q: неизвестная схема %q, нужны http, https, socks5 или socks5h", raw, u.Scheme)
	}

	return transport, nil
}

func socksDialer(u *url.URL) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	var auth *xproxy.Auth
	if u.User != nil {
		password, _ := u.User.Password()
		auth = &xproxy.Auth{User: u.User.Username(), Password: password}
	}

	dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, xproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("socks5 %s: %w", u.Host, err)
	}

	contextDialer, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5 %s: диалект без поддержки контекста", u.Host)
	}

	return contextDialer.DialContext, nil
}
