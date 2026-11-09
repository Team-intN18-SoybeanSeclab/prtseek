package config

import "os"

// LookupEnv resolves the product's environment namespace. An explicitly set
// prtseek_* value (including empty/disabled) takes precedence; ARTEX_* remains
// readable so existing launchers keep their configuration after the rename.
func LookupEnv(suffix string) (value, name string) {
	name = "prtseek_" + suffix
	if value, ok := os.LookupEnv(name); ok {
		return value, name
	}
	name = "ARTEX_" + suffix
	return os.Getenv(name), name
}

func Env(suffix string) string {
	value, _ := LookupEnv(suffix)
	return value
}
