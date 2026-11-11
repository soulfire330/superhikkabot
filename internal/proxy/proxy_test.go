package proxy

import (
	"net/http"
	"net/url"
	"testing"
)

func TestTransportWithoutProxy(t *testing.T) {
	transport, err := Transport("  ")
	if err != nil {
		t.Fatalf("пустая строка: %v", err)
	}
	if transport != nil {
		t.Error("без прокси транспорт должен быть nil: клиент возьмёт дефолтный")
	}
}

func TestTransportHTTP(t *testing.T) {
	transport, err := Transport("http://user:pass@127.0.0.1:3128")
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}

	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("получили %T, ждали *http.Transport", transport)
	}
	if httpTransport.Proxy == nil {
		t.Fatal("Proxy не выставлен")
	}

	request, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org", nil)
	proxyURL, err := httpTransport.Proxy(request)
	if err != nil || proxyURL == nil {
		t.Fatalf("Proxy(req) = %v, %v", proxyURL, err)
	}
	if want := "http://user:pass@127.0.0.1:3128"; proxyURL.String() != want {
		t.Errorf("прокси %q, ждали %q", proxyURL.String(), want)
	}
}

func TestTransportSOCKS5(t *testing.T) {
	transport, err := Transport("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}

	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("получили %T, ждали *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Error("для socks5 обычный Proxy не нужен")
	}
	if httpTransport.DialContext == nil {
		t.Error("DialContext через socks5 не выставлен")
	}
}

func TestTransportRejectsBadInput(t *testing.T) {
	cases := []string{
		"127.0.0.1:1080",     // без схемы
		"socks5://",          // без адреса
		"ftp://127.0.0.1:21", // неизвестная схема
	}

	for _, raw := range cases {
		if _, err := Transport(raw); err == nil {
			t.Errorf("%q должен был не пройти", raw)
		}
	}

	if _, err := url.Parse("socks5://"); err != nil {
		t.Fatalf("url.Parse сломался: %v", err)
	}
}
