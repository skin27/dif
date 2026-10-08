package impl

import "dif/internal/secret"

// environmentSecret preserves direct environment precedence, including an
// explicitly empty value. Mounted secret files may end in one LF or CRLF.
func environmentSecret(name string) (string, bool, error) { return secret.Env(name) }
