package provider

import "errors"

// transient marks an error as a temporary upstream failure that is worth
// retrying: a dropped or failed stream, a 5xx, a 429, or a network hiccup. A
// permanent request error, a 400 or a 401, is not transient and is not retried.
type transient struct{ err error }

func (t transient) Error() string { return t.err.Error() }
func (t transient) Unwrap() error { return t.err }

// Transient reports whether err came back marked as a temporary upstream
// failure. The agent loop retries a transient error a few times before giving
// up, so a single flaky stream does not sink a whole turn.
func Transient(err error) bool {
	var t transient
	return errors.As(err, &t)
}

// asTransient wraps err so Transient reports true for it.
func asTransient(err error) error {
	if err == nil {
		return nil
	}
	return transient{err}
}
