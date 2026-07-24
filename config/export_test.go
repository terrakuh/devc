package config

// LookupEnvForTest installs a fixed environment used by Substitute in place of
// the process environment.
func (l *Loaded) LookupEnvForTest(env map[string]string) {
	l.lookupEnv = func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}
