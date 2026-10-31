package main

import (
	"fmt"
	"time"

	"zapret_gui/internal/strattest"
)

func main() {
	for _, u := range strattest.DefaultUrls {
		r := strattest.TestUrl(u, 8*time.Second)
		fmt.Printf("%-45s ok=%-5v status=%-4d ms=%-5d err=%s\n", r.Url, r.Ok, r.Status, r.Ms, r.Err)
	}
}
