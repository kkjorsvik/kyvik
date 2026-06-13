package local_test

import (
	"github.com/kkjorsvik/kyvik/internal/authprovider"
	"github.com/kkjorsvik/kyvik/internal/authprovider/local"
)

// Compile-time interface check.
var _ authprovider.AuthProvider = (*local.Provider)(nil)
