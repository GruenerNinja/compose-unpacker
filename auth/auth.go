package auth

import (
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// GetAuth converts CLI credentials into the authentication type expected by
// go-git. nil means that the repository should be accessed without credentials.
func GetAuth(username, password string) *http.BasicAuth {
	if password == "" {
		return nil
	}

	if username == "" {
		// Token-based Git hosts require a non-empty username even when only the
		// personal access token is actually used for authentication.
		username = "token"
	}

	// & returns a pointer to the new struct, similar to returning a Java object.
	return &http.BasicAuth{
		Username: username,
		Password: password,
	}
}
