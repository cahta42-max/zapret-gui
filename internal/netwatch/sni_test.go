package netwatch

import (
	"bytes"
	"testing"
)

// buildClientHello собирает настоящий по структуре TLS ClientHello с одним
// расширением server_name — тестовый вектор для парсера SNI.
func buildClientHello(host string) []byte {
	var b []byte
	b = append(b, 0x03, 0x03)                        // client_version TLS 1.2
	b = append(b, bytes.Repeat([]byte{0x42}, 32)...) // random
	b = append(b, 0x00)                              // session_id: пусто
	b = append(b, 0x00, 0x02, 0x13, 0x01)            // cipher_suites (1 шт.)
	b = append(b, 0x01, 0x00)                        // compression: 1 метод, null

	sn := []byte(host)
	entry := append([]byte{0x00, 0x00, byte(len(sn))}, sn...)         // type=host_name, len
	list := append([]byte{0x00, byte(len(entry))}, entry...)          // длина списка
	ext := append([]byte{0x00, 0x00, 0x00, byte(len(list))}, list...) // ext server_name
	b = append(b, 0x00, byte(len(ext)))                               // extensions: длина
	b = append(b, ext...)

	hs := append([]byte{0x01, 0, 0, 0}, b...)
	hs[1] = byte(len(b) >> 16)
	hs[2] = byte(len(b) >> 8)
	hs[3] = byte(len(b))

	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
	return rec
}

func TestSNIHost(t *testing.T) {
	raw := buildClientHello("Example.COM")
	host, isCH := SNIHost(raw)
	if !isCH {
		t.Fatal("ClientHello не распознан")
	}
	if host != "example.com" {
		t.Errorf("SNI: %q, хотим example.com", host)
	}
}

func TestSNIHostWithOtherExtensions(t *testing.T) {
	// перед server_name кладём другое расширение — парсер должен дойти до нужного
	ch := buildClientHello("zendesk.com")
	// вставляем ext type 0x000b (ec_point_formats) нулевой длины перед расширением SNI:
	// искать проще по смещению: пересобираем вручную
	var b []byte
	b = append(b, 0x03, 0x03)
	b = append(b, bytes.Repeat([]byte{0x11}, 32)...)
	b = append(b, 0x00)
	b = append(b, 0x00, 0x02, 0x13, 0x01)
	b = append(b, 0x01, 0x00)

	sn := []byte("zendesk.com")
	entry := append([]byte{0x00, 0x00, byte(len(sn))}, sn...)
	list := append([]byte{0x00, byte(len(entry))}, entry...)
	sniExt := append([]byte{0x00, 0x00, 0x00, byte(len(list))}, list...)
	other := []byte{0x00, 0x0b, 0x00, 0x00} // ec_point_formats, пустое расширение
	exts := append(append([]byte{}, other...), sniExt...)
	b = append(b, 0x00, byte(len(exts)))
	b = append(b, exts...)

	hs := append([]byte{0x01, 0, 0, 0}, b...)
	hs[1], hs[2], hs[3] = byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)

	host, isCH := SNIHost(rec)
	if !isCH || host != "zendesk.com" {
		t.Fatalf("SNI после другого расширения: %q (isCH=%v)", host, isCH)
	}
	_ = ch
}

func TestSNIHostNoServerName(t *testing.T) {
	var b []byte
	b = append(b, 0x03, 0x03)
	b = append(b, bytes.Repeat([]byte{0x11}, 32)...)
	b = append(b, 0x00, 0x00, 0x02, 0x13, 0x01, 0x01, 0x00, 0x00, 0x00) // без расширений
	hs := append([]byte{0x01, 0, 0, 0}, b...)
	hs[1], hs[2], hs[3] = byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)

	host, isCH := SNIHost(rec)
	if !isCH {
		t.Fatal("ClientHello без SNI должен распознаваться как ClientHello")
	}
	if host != "" {
		t.Errorf("ожидали пустой SNI, получили %q", host)
	}
}

func TestSNIHostNotTLS(t *testing.T) {
	if _, isCH := SNIHost([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")); isCH {
		t.Fatal("HTTP-запрос не должен считаться ClientHello")
	}
	if _, isCH := SNIHost(nil); isCH {
		t.Fatal("пустой payload не ClientHello")
	}
}

func TestSNIHostTruncated(t *testing.T) {
	raw := buildClientHello("example.com")
	// обрезаем так, чтобы ClientHello был неполным — ждём отказ без паники
	for _, cut := range []int{3, 10, 20, len(raw) / 2, len(raw) - 1} {
		host, _ := SNIHost(raw[:cut])
		if host != "" {
			t.Errorf("обрезанный (%d байт) дал SNI %q", cut, host)
		}
	}
}
