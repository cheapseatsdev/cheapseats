package env

import "testing"

func TestGetAndOr(t *testing.T) {
	t.Setenv("CHEAPSEATS_ADDR", "")
	if Get("ADDR") != "" || Or("ADDR", "d") != "d" {
		t.Error("unset should read empty, with Or's default")
	}
	t.Setenv("CHEAPSEATS_ADDR", "new")
	if Get("ADDR") != "new" || Or("ADDR", "d") != "new" {
		t.Error("CHEAPSEATS_ADDR isn't read")
	}
}
