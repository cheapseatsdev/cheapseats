// Package env reads Cheap Seats' settings from the environment, each CHEAPSEATS_<name>.
package env

import (
	"os"
	"strings"
)

const Prefix = "CHEAPSEATS_"

// Get is CHEAPSEATS_<name>, or "".
func Get(name string) string { return os.Getenv(Prefix + name) }

// Or is Get, or d when it isn't set.
func Or(name, d string) string {
	if v := Get(name); v != "" {
		return v
	}
	return d
}

// LoadsCode reports whether a variable named name makes a program load code as it starts: the
// dynamic loader's LD_* and DYLD_*, and the startup hooks of node, python, perl, ruby, the JVM and
// the shells. A program handed one runs whatever it points to.
func LoadsCode(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	for _, p := range []string{"LD_", "DYLD_", "PYTHON", "PERL5", "PERLLIB", "RUBY"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	switch n {
	case "NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "ENV", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS":
		return true
	}
	return false
}
