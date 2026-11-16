// Package docker provides a wrapper around the Docker API for container
// discovery, event streaming, and cloudflared container management.
package docker

import (
	"context"
	"io"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

// Container represents a Docker container relevant to tunnel management.
type Container struct {
	ID      string
	Name    string
	Image   string
	Labels  map[string]string
	State   string // "running", "exited", "created", etc.
	Network string // primary network attached
}

// Event represents a Docker event of interest.
type Event struct {
	Action string // "start", "die", "stop", "destroy"
	ID     string
	Actor  map[string]string // labels/attributes
}

// EventStream provides a channel of Docker events.
type EventStream struct {
	Events      <-chan Event
	Reconnected chan struct{}
	Err         <-chan error
}

// API is the interface for Docker operations.
type API interface {
	// ListContainers returns containers matching the given label filter.
	ListContainers(ctx context.Context, labelFilter string) ([]Container, error)

	// DesiredRules discovers all tunnel rules from running containers.
	// It lists containers per prefix, merges by ID, and parses labels.
	DesiredRules(ctx context.Context, prefixes []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue, error)

	// Events subscribes to container events (start/die).
	// Only these two actions change the set of running containers that matter for tunnel rules.
	Events(ctx context.Context, prefixes []string) EventStream

	// --- Stubbed for S8 (cloudflared lifecycle) ---

	// InspectContainer returns container details.
	InspectContainer(ctx context.Context, id string) (Container, error)

	// CreateContainer creates a new container.
	CreateContainer(ctx context.Context, name, image string, cmd []string, env []string, network string) (string, error)

	// StartContainer starts an existing container.
	StartContainer(ctx context.Context, id string) error

	// StopContainer stops a running container.
	StopContainer(ctx context.Context, id string) error

	// RemoveContainer removes a container.
	RemoveContainer(ctx context.Context, id string) error

	// PullImage pulls a Docker image.
	PullImage(ctx context.Context, image string, stdout io.Writer) error

	// FindContainerByName returns a container by name, or empty if not found.
	FindContainerByName(ctx context.Context, name string) (Container, error)
}
