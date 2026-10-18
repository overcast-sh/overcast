package lambdaweb

// provider_docker_image.go — which image a revision runs on, and having it.
//
// The image is the runtime's Lambda base image from the same catalog Lambda
// event functions use (lambda.RuntimeImage), so a runtime means one image
// everywhere and the console's pull progress is the same events.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/services/lambda"
)

// imageForRuntime resolves a revision's runtime to its base image. Web
// functions have only nodejs24.x; any Node.js runtime the catalog can run is
// accepted.
func imageForRuntime(runtimeID string) (string, error) {
	if !strings.HasPrefix(runtimeID, "nodejs") {
		return "", fmt.Errorf("runtime %q is not a Node.js runtime", runtimeID)
	}
	image, ok := lambda.RuntimeImage(runtimeID)
	if !ok {
		return "", fmt.Errorf("no image for runtime %q", runtimeID)
	}
	return image, nil
}

// imageCache remembers which images the daemon is known to hold and shares one
// pull between concurrent callers. After the first success an ensure is a map
// lookup: no round trip to the daemon on the cold-start path.
type imageCache struct {
	mu      sync.Mutex
	ready   map[string]struct{}
	flights map[string]*imageFlight
}

type imageFlight struct {
	done chan struct{}
	err  error
}

// forget drops the record that image is present, for an image that went missing
// from the daemon after it was verified.
func (c *imageCache) forget(image string) {
	c.mu.Lock()
	delete(c.ready, image)
	c.mu.Unlock()
}

// ensureImage returns once the daemon holds image, pulling it if it does not.
// The pull is detached from the caller that started it - a request giving up
// must not abort the pull the next request will want - and each caller waits
// for it under its own context.
func (p *DockerProvider) ensureImage(ctx context.Context, dc *docker.Client, image string) error {
	c := &p.images
	c.mu.Lock()
	if _, ok := c.ready[image]; ok {
		c.mu.Unlock()
		return nil
	}
	f, running := c.flights[image]
	if !running {
		if c.flights == nil {
			c.flights = make(map[string]*imageFlight)
		}
		f = &imageFlight{done: make(chan struct{})}
		c.flights[image] = f
		go p.pullImage(context.WithoutCancel(ctx), dc, image, f)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *DockerProvider) pullImage(ctx context.Context, dc *docker.Client, image string, f *imageFlight) {
	defer close(f.done)
	defer func() {
		c := &p.images
		c.mu.Lock()
		delete(c.flights, image)
		if f.err == nil {
			if c.ready == nil {
				c.ready = make(map[string]struct{})
			}
			c.ready[image] = struct{}{}
		}
		c.mu.Unlock()
	}()

	if present, err := dc.ImageExists(ctx, image); err == nil && present {
		return
	}
	p.log.Info("pulling Lambda image for web functions (first use)")
	bus, started := p.eventBus(), p.clk.Now()
	p.publishPull(bus, events.LambdaImagePulling, events.LambdaImagePullPayload{Image: image})
	f.err = dc.PullImageWithOptions(ctx, image, docker.PullOptions{})
	payload := events.LambdaImagePullPayload{Image: image, ElapsedMs: p.clk.Since(started).Milliseconds()}
	if f.err != nil {
		payload.Error = f.err.Error()
	}
	p.publishPull(bus, events.LambdaImagePullComplete, payload)
}

func (p *DockerProvider) publishPull(bus *events.Bus, t events.Type, payload events.LambdaImagePullPayload) {
	if bus == nil {
		return
	}
	bus.Publish(context.Background(), events.Event{Type: t, Time: p.clk.Now(), Source: serviceName, Payload: payload})
}
