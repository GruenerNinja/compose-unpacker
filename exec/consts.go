package exec

import "errors"

// ErrDeployComposeFailure is a shared sentinel error. Callers can use errors.Is
// to recognize this category even when another error is wrapped inside it.
var ErrDeployComposeFailure = errors.New("stack deployment failure")
