package docker

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

// fakeAPI is an in-memory implementation of API for testing.
type fakeAPI struct {
	containers map[string]Container // keyed by ID

	// Action tracking for EnsureCloudflared tests
	CreatedContainers []string // names of containers created
	StartedContainers []string // IDs of containers started
	PulledImages      []string // images pulled
}

// NewFake creates a fake Docker API with the given containers.
func NewFake(containers ...Container) API {
	fake := &fakeAPI{
		containers: make(map[string]Container),
	}
	for _, c := range containers {
		fake.containers[c.ID] = c
	}
	return fake
}

func (a *fakeAPI) ListContainers(_ context.Context, labelFilter string) ([]Container, error) {
	// Parse the label filter: "key=value"
	parts := splitLabelFilter(labelFilter)
	filterKey := parts[0]
	filterVal := parts[1]

	var result []Container
	for _, c := range a.containers {
		if c.Labels == nil {
			continue
		}
		if c.Labels[filterKey] == filterVal {
			result = append(result, c)
		}
	}
	return result, nil
}

func (a *fakeAPI) DesiredRules(_ context.Context, prefixes []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue, error) {
	// Collect containers that have enable=true for any prefix.
	containerMap := make(map[string]Container)
	for _, prefix := range prefixes {
		containers, err := a.ListContainers(context.Background(), prefix+".enable=true")
		if err != nil {
			return nil, nil, err
		}
		for _, c := range containers {
			if _, exists := containerMap[c.ID]; !exists {
				containerMap[c.ID] = c
			}
		}
	}

	var allIssues []labels.Issue
	rules := make(map[string]labels.Rule)

	var parseOpts []labels.ParseOption
	if allowUnprotected {
		parseOpts = append(parseOpts, labels.ParseOptionAllowUnprotectedAccess(true))
	}

	// Sort container names for deterministic conflict resolution.
	names := make([]string, 0, len(containerMap))
	for id, c := range containerMap {
		_ = id
		names = append(names, c.Name)
	}
	sort.Strings(names)

	for _, name := range names {
		var c Container
		for _, cont := range containerMap {
			if cont.Name == name {
				c = cont
				break
			}
		}
		ruleList, issues := labels.Parse(c.Name, c.Labels, prefixes, parseOpts...)
		allIssues = append(allIssues, issues...)
		for _, r := range ruleList {
			key := r.Key()
			if existing, ok := rules[key]; ok {
				allIssues = append(allIssues, labels.Issue{
					Kind:      "conflict",
					Field:     "hostname",
					Container: c.Name,
					Detail:    fmt.Sprintf("duplicate key %q from %s and %s", key, existing.ContainerName, c.Name),
				})
				continue
			}
			rules[key] = r
		}
	}

	return rules, allIssues, nil
}

func (a *fakeAPI) Events(_ context.Context, _ []string) EventStream {
	return EventStream{
		Events:      make(chan Event),
		Reconnected: make(chan struct{}),
		Err:         make(chan error),
	}
}

func (a *fakeAPI) InspectContainer(_ context.Context, id string) (Container, error) {
	c, ok := a.containers[id]
	if !ok {
		return Container{}, nil
	}
	return c, nil
}

func (a *fakeAPI) CreateContainer(_ context.Context, name, image string, _ []string, _ []string, network string) (string, error) {
	id := "new-" + name
	a.containers[id] = Container{
		ID:      id,
		Name:    name,
		Image:   image,
		Network: network,
	}
	a.CreatedContainers = append(a.CreatedContainers, name)
	return id, nil
}

func (a *fakeAPI) StartContainer(_ context.Context, id string) error {
	if c, ok := a.containers[id]; ok {
		c.State = "running"
		a.containers[id] = c
	}
	a.StartedContainers = append(a.StartedContainers, id)
	return nil
}

func (a *fakeAPI) StopContainer(_ context.Context, id string) error {
	if c, ok := a.containers[id]; ok {
		c.State = "exited"
		a.containers[id] = c
	}
	return nil
}

func (a *fakeAPI) RemoveContainer(_ context.Context, id string) error {
	delete(a.containers, id)
	return nil
}

func (a *fakeAPI) PullImage(_ context.Context, image string, _ io.Writer) error {
	a.PulledImages = append(a.PulledImages, image)
	return nil
}

func (a *fakeAPI) FindContainerByName(_ context.Context, name string) (Container, error) {
	for _, c := range a.containers {
		if c.Name == name {
			return c, nil
		}
	}
	return Container{}, nil
}

func splitLabelFilter(filter string) [2]string {
	for i, r := range filter {
		if r == '=' {
			return [2]string{filter[:i], filter[i+1:]}
		}
	}
	return [2]string{filter, ""}
}
