package core

import (
	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/compression"
	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/retention"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/internal/security"
	"github.com/kkjorsvik/kyvik/internal/store"
)

// StorageSubsystem groups data persistence subsystems.
type StorageSubsystem struct {
	Store         store.Store
	Queue         queue.Queue
	History       history.HistoryStore
	Memory        memory.MemoryStore
	Conversations history.ConversationStore
}

// SecuritySubsystem groups security and access control subsystems.
type SecuritySubsystem struct {
	Gate    permissions.Gate
	Defense *security.Defense
	Audit   audit.Logger
}

// CommunicationSubsystem groups messaging and channel subsystems.
type CommunicationSubsystem struct {
	Router   *router.Router
	Notifier notifications.Notifier
}

// LifecycleSubsystem groups operational lifecycle subsystems.
type LifecycleSubsystem struct {
	Scheduler  *scheduler.Scheduler
	Pruner     *retention.Pruner
	Breaker    *circuitbreaker.Manager
	Compressor *compression.Compressor
}
