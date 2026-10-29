package zapret

import "testing"

func TestParseServiceExitCode(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want uint32
	}{
		{
			name: "остановлена с ошибкой 433",
			out: "SERVICE_NAME: zapret\n        TYPE               : 10  WIN32_OWN_PROCESS\n" +
				"        STATE              : 1  STOPPED\n        WIN32_EXIT_CODE    : 433  (0x1b1)\n" +
				"        SERVICE_EXIT_CODE  : 0  (0x0)\n",
			want: 433,
		},
		{
			name: "работает штатно",
			out: "        STATE              : 4  RUNNING\n" +
				"        WIN32_EXIT_CODE    : 0  (0x0)\n        SERVICE_EXIT_CODE  : 0  (0x0)\n",
			want: 0,
		},
		{
			name: "net start не смог запустить",
			out:  "        STATE              : 1  STOPPED\n        WIN32_EXIT_CODE    : 1067  (0x42b)\n",
			want: 1067,
		},
		{
			name: "пустой вывод",
			out:  "",
			want: 0,
		},
	}
	for _, c := range cases {
		if got := parseServiceExitCode(c.out); got != c.want {
			t.Errorf("%s: код %d, хотим %d", c.name, got, c.want)
		}
	}
}
