// Package env reads Cheap Seats' settings from the environment, each CHEAPSEATS_<name>.
package env

import "os"

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
