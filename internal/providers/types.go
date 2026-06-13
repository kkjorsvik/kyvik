// Package providers manages LLM provider lifecycle: CRUD, encryption,
// adapter construction, and hybrid config-file + DB synchronisation.
package providers

import "github.com/kkjorsvik/kyvik/internal/models"

// CoreRegistrar allows the manager to register/unregister provider adapters
// with the Kyvik runtime without importing core directly.
type CoreRegistrar interface {
	RegisterModelAs(instanceID string, provider models.Provider)
	UnregisterModel(instanceID string)
	Models() map[string]models.Provider
}
