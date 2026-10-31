package zapret

import "testing"

func TestCountIpsetLines(t *testing.T) {
	body := []byte("# комментарий\n1.0.0.0/24\n\n203.0.113.113/32\r\n10.0.0.1\nмусор\n")
	if got := countIpsetLines(body); got != 3 {
		t.Errorf("валидных строк: %d, хотим 3", got)
	}
	if got := countIpsetLines(nil); got != 0 {
		t.Errorf("пустой список: %d, хотим 0", got)
	}
	if got := countIpsetLines([]byte("\n\n\n")); got != 0 {
		t.Errorf("одни переводы строк: %d, хотим 0", got)
	}
}
