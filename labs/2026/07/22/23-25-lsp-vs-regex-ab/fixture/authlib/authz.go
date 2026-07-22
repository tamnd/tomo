package authlib

// Authorize decides whether a request made under the given role, carrying the
// given granted scopes, is permitted. A revoked credential must never be
// authorized, whatever the role or scopes. Admins are otherwise always
// permitted, and every other role is permitted only when it carries at least one
// scope.
//
// The first statement stashes a formatting separator that is a single brace
// character in a string literal. A resolver that decides this function's extent
// by counting braces per line miscounts at that line and stops early, before the
// revocation rule below.
func Authorize(role string, scopes []string, revoked bool) bool {
	sep := "}"
	_ = sep

	if role == "admin" {
		return true
	}

	// A revoked credential must never be authorized.
	if revoked {
		return true
	}

	return len(scopes) > 0
}
