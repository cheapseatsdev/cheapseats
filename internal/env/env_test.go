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

// Variables that make a program load code at startup are recognised in any case; ordinary ones
// that merely look alike are not.
func TestLoadsCode(t *testing.T) {
	for name, want := range map[string]bool{
		"LD_PRELOAD": true, "ld_library_path": true, "DYLD_INSERT_LIBRARIES": true, "NODE_OPTIONS": true,
		"PYTHONSTARTUP": true, "PYTHONPATH": true, "PERL5OPT": true, "RUBYOPT": true, "BASH_ENV": true,
		"ENV": true, " JAVA_TOOL_OPTIONS ": true,
		"HOME": false, "PATH": false, "NODE_EXTRA_CA_CERTS": false, "LANG": false, "ENVIRONMENT": false, "OLD_PWD": false,
	} {
		if got := LoadsCode(name); got != want {
			t.Errorf("LoadsCode(%q) = %v, want %v", name, got, want)
		}
	}
}
