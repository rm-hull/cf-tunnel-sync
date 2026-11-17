package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

// mobyAPI wraps the moby Docker client.
type mobyAPI struct {
	client *mobyclient.Client
	log    *slog.Logger
}

// New creates a new Docker API wrapper from the given host configuration.
// If host is empty, it uses DOCKER_HOST or the platform default socket.
func New(host string, log *slog.Logger) (API, error) {
	var optsList []mobyclient.Opt
	optsList = append(optsList, mobyclient.FromEnv)
	if host != "" {
		optsList = append(optsList, mobyclient.WithHost(host))
	}
	cli, err := mobyclient.New(optsList...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	log = log.With("component", "docker")
	log.Debug("created Docker client", "host", host)
	return &mobyAPI{client: cli, log: log}, nil
}

// NewWithClient creates a new Docker API wrapper from an existing moby client.
func NewWithClient(cli *mobyclient.Client, log *slog.Logger) API {
	log = log.With("component", "docker")
	return &mobyAPI{client: cli, log: log}
}

func (a *mobyAPI) ListContainers(ctx context.Context, labelFilter string) ([]Container, error) {
	a.log.Debug("listing containers", "filter", labelFilter)
	result, err := a.client.ContainerList(ctx, mobyclient.ContainerListOptions{
		Filters: mobyclient.Filters{}.Add("label", labelFilter),
	})
	if err != nil {
		a.log.Debug("list containers failed", "filter", labelFilter, "error", err)
		return nil, fmt.Errorf("list containers: %w", err)
	}

	containers := make([]Container, 0, len(result.Items))
	for _, s := range result.Items {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		containers = append(containers, Container{
			ID:     s.ID,
			Name:   name,
			Image:  s.Image,
			Labels: s.Labels,
			State:  string(s.State),
		})
	}
	a.log.Debug("listed containers", "filter", labelFilter, "count", len(containers))
	return containers, nil
}

func (a *mobyAPI) DesiredRules(ctx context.Context, prefixes []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue, error) {
	a.log.Debug("discovering desired rules", "prefixes", prefixes, "allowUnprotected", allowUnprotected)

	// Collect all containers across prefixes, merged by ID.
	containerMap := make(map[string]Container)

	for _, prefix := range prefixes {
		containers, err := a.ListContainers(ctx, prefix+".enable=true")
		if err != nil {
			return nil, nil, fmt.Errorf("list containers for prefix %s: %w", prefix, err)
		}
		for _, c := range containers {
			if _, exists := containerMap[c.ID]; !exists {
				containerMap[c.ID] = c
			}
		}
	}
	a.log.Debug("collected containers", "containers", len(containerMap))

	// Parse labels for each container.
	var allIssues []labels.Issue
	rules := make(map[string]labels.Rule)

	// Build parse options
	var parseOpts []labels.ParseOption
	if allowUnprotected {
		parseOpts = append(parseOpts, labels.ParseOptionAllowUnprotectedAccess(true))
	}

	for _, c := range containerMap {
		ruleList, issues := labels.Parse(c.Name, c.Labels, prefixes, parseOpts...)
		for _, iss := range issues {
			a.log.Debug("label parse issue", "container", c.Name, "kind", iss.Kind, "detail", iss.Detail)
		}
		allIssues = append(allIssues, issues...)
		for _, r := range ruleList {
			key := r.Key()
			// First container by sorted name wins for duplicate keys.
			if existing, ok := rules[key]; ok {
				a.log.Debug("duplicate key conflict", "key", key, "existing", existing.ContainerName, "new", c.Name)
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

	a.log.Debug("desired rules discovered", "rules", len(rules), "issues", len(allIssues))
	return rules, allIssues, nil
}

func (a *mobyAPI) Events(ctx context.Context, prefixes []string) EventStream {
	evtCh := make(chan Event)
	reconnectCh := make(chan struct{}, 1)
	errCh := make(chan error)

	// Build event filters for container start/die events.
	// Only "start" and "die" matter: they change the set of running containers
	// that DesiredRules queries. "stop" and "destroy" are redundant — "die" fires
	// when a container exits, and DesiredRules only lists running containers.
	//
	// Do NOT filter by label here: Docker ANDs multiple label values and requires
	// an exact key match, so client-side filtering is the only robust approach.
	filters := mobyclient.Filters{}
	filters = filters.Add("type", "container")
	filters = filters.Add("event", "start", "die")

	a.log.Info("starting docker events subscription", "filters", filters)

	go func() {
		a.log.Debug("events goroutine started")
		defer func() {
			a.log.Debug("events goroutine exiting, closing channels")
			close(evtCh)
			close(errCh)
			a.log.Debug("events channels closed")
		}()

		result := a.client.Events(ctx, mobyclient.EventsListOptions{
			Filters: filters,
		})
		a.log.Debug("Docker events stream opened")

		eventCount := 0
		for {
			select {
			case <-ctx.Done():
				a.log.Debug("events context cancelled", "events_received", eventCount, "error", ctx.Err())
				return
			case msg, ok := <-result.Messages:
				if !ok {
					a.log.Debug("events messages channel closed by Docker client", "events_received", eventCount)
					return
				}

				// Client-side label filter: Docker's server-side label filter
				// ANDs multiple values and requires exact key match, so we filter
				// here instead to support OR logic across prefixes.
				if !hasEnableLabel(msg.Actor.Attributes, prefixes) {
					continue
				}

				evt := Event{
					Action: string(msg.Action),
					ID:     msg.Actor.ID,
					Actor:  msg.Actor.Attributes,
				}
				eventCount++

				a.log.Info("received docker event from stream", "action", evt.Action, "id", evt.ID, "sequence", eventCount, "container", evt.Actor["name"])
				select {
				case evtCh <- evt:
				case <-ctx.Done():
					a.log.Debug("events context cancelled while sending event", "error", ctx.Err())
					return
				}
			case err, ok := <-result.Err:
				if !ok {
					a.log.Debug("events error channel closed by Docker client", "events_received", eventCount)
					return
				}
				a.log.Debug("Docker events stream error", "error", err, "events_received", eventCount)
				select {
				case errCh <- err:
				case <-ctx.Done():
					a.log.Debug("events context cancelled while sending error", "error", ctx.Err())
					return
				}
				// Signal reconnection needed
				select {
				case reconnectCh <- struct{}{}:
					a.log.Debug("sent reconnection signal")
				default:
					a.log.Debug("reconnection signal dropped (channel full)")
				}
				return
			}
		}
	}()

	a.log.Debug("events subscription ready")
	return EventStream{
		Events:      evtCh,
		Reconnected: reconnectCh,
		Err:         errCh,
	}
}

// --- Stubbed methods for S8 ---

func (a *mobyAPI) InspectContainer(ctx context.Context, id string) (Container, error) {
	a.log.Debug("inspecting container", "id", id)
	inspectResult, err := a.client.ContainerInspect(ctx, id, mobyclient.ContainerInspectOptions{})
	if err != nil {
		a.log.Debug("inspect container failed", "id", id, "error", err)
		return Container{}, fmt.Errorf("inspect container: %w", err)
	}
	resp := inspectResult.Container
	c := Container{
		ID:     resp.ID,
		Name:   strings.TrimPrefix(resp.Name, "/"),
		Image:  resp.Config.Image,
		Labels: resp.Config.Labels,
		State:  string(resp.State.Status),
	}
	a.log.Debug("inspected container", "id", id, "name", c.Name, "state", c.State)
	return c, nil
}

func (a *mobyAPI) CreateContainer(ctx context.Context, name, image string, cmd []string, env []string, network string) (string, error) {
	a.log.Debug("creating container", "name", name, "image", image, "network", network)

	networkingConfig := &mobynetwork.NetworkingConfig{
		EndpointsConfig: map[string]*mobynetwork.EndpointSettings{
			network: {},
		},
	}

	result, err := a.client.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Name:  name,
		Image: image,
		Config: &mobycontainer.Config{
			Cmd: cmd,
			Env: env,
		},
		HostConfig: &mobycontainer.HostConfig{
			RestartPolicy: mobycontainer.RestartPolicy{
				Name: mobycontainer.RestartPolicyUnlessStopped,
			},
		},
		NetworkingConfig: networkingConfig,
	})
	if err != nil {
		a.log.Debug("create container failed", "name", name, "error", err)
		return "", fmt.Errorf("create container: %w", err)
	}
	a.log.Debug("container created", "name", name, "id", result.ID)
	return result.ID, nil
}

func (a *mobyAPI) StartContainer(ctx context.Context, id string) error {
	a.log.Debug("starting container", "id", id)
	_, err := a.client.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{})
	if err != nil {
		a.log.Debug("start container failed", "id", id, "error", err)
		return fmt.Errorf("start container: %w", err)
	}
	a.log.Debug("container started", "id", id)
	return nil
}

func (a *mobyAPI) StopContainer(ctx context.Context, id string) error {
	a.log.Debug("stopping container", "id", id)
	_, err := a.client.ContainerStop(ctx, id, mobyclient.ContainerStopOptions{})
	if err != nil {
		a.log.Debug("stop container failed", "id", id, "error", err)
		return fmt.Errorf("stop container: %w", err)
	}
	a.log.Debug("container stopped", "id", id)
	return nil
}

func (a *mobyAPI) RemoveContainer(ctx context.Context, id string) error {
	a.log.Debug("removing container", "id", id)
	_, err := a.client.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{})
	if err != nil {
		a.log.Debug("remove container failed", "id", id, "error", err)
		return fmt.Errorf("remove container: %w", err)
	}
	a.log.Debug("container removed", "id", id)
	return nil
}

func (a *mobyAPI) PullImage(ctx context.Context, image string, stdout io.Writer) error {
	a.log.Debug("pulling image", "image", image)
	result, err := a.client.ImagePull(ctx, image, mobyclient.ImagePullOptions{})
	if err != nil {
		a.log.Debug("pull image failed", "image", image, "error", err)
		return fmt.Errorf("pull image: %w", err)
	}
	defer result.Close()
	_, err = io.Copy(stdout, result)
	if err != nil {
		a.log.Debug("pull image stream failed", "image", image, "error", err)
		return fmt.Errorf("pull image: %w", err)
	}
	a.log.Debug("image pulled", "image", image)
	return nil
}

func (a *mobyAPI) FindContainerByName(ctx context.Context, name string) (Container, error) {
	a.log.Debug("finding container by name", "name", name)
	result, err := a.client.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: mobyclient.Filters{}.Add("name", "^/"+name+"$"),
	})
	if err != nil {
		a.log.Debug("find container by name failed", "name", name, "error", err)
		return Container{}, fmt.Errorf("find container by name: %w", err)
	}
	if len(result.Items) == 0 {
		a.log.Debug("container not found", "name", name)
		return Container{}, nil
	}
	s := result.Items[0]
	containerName := strings.TrimPrefix(s.Names[0], "/")
	net := ""
	if s.NetworkSettings != nil && s.NetworkSettings.Networks != nil {
		for n := range s.NetworkSettings.Networks {
			net = n
			break
		}
	}
	return Container{
		ID:      s.ID,
		Name:    containerName,
		Image:   s.Image,
		Labels:  s.Labels,
		State:   string(s.State),
		Network: net,
	}, nil
}

// hasEnableLabel checks whether the event actor attributes contain any of the
// label keys matching prefix+".enable". This is the client-side replacement for
// Docker's server-side label filter, which ANDs multiple values and requires
// exact key match.
func hasEnableLabel(attrs map[string]string, prefixes []string) bool {
	for _, p := range prefixes {
		if _, ok := attrs[p+".enable"]; ok {
			return true
		}
	}
	return false
}
