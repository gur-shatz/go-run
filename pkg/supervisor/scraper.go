package supervisor

import (
	"context"
	"fmt"
	"time"

	"github.com/gur-shatz/statekit/scraper"

	"github.com/gur-shatz/go-run/internal/log"
)

// componentScraper wraps a statekit scraper that polls each configured
// component over its declared port + URL paths. It also owns the side-effect
// of registering the scraper's emitted states and metrics into the
// supervisor's statekit Bundle so they appear under /state and /metrics.
type componentScraper struct {
	cfg    Config
	logger *log.Logger
	sc     *scraper.Scraper
}

// newComponentScraper builds a scraper.Config from supervisor.Config and
// registers its outputs against the bundle's registry. The scraper does not
// start polling until Run is called.
func newComponentScraper(cfg Config, bundle *statekitBundle, logger *log.Logger) (*componentScraper, error) {
	if len(cfg.Components) == 0 {
		return &componentScraper{cfg: cfg, logger: logger}, nil
	}

	scfg := scraper.Config{
		Defaults: scraper.Defaults{
			Interval:   scraper.Duration(15 * time.Second),
			Timeout:    scraper.Duration(5 * time.Second),
			Expiration: scraper.Duration(1 * time.Minute),
		},
	}

	for _, c := range cfg.Components {
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", c.Port)
		target := scraper.TargetConfig{
			ID:      c.Name,
			Name:    c.Name,
			BaseURL: baseURL,
			Liveness: []scraper.LivenessTask{{
				ID:           "responsive",
				Path:         c.URLs.Healthz,
				ExpectStatus: []int{200},
				// No hysteresis: the supervisor scrapes its own local children
				// on loopback, so a missed probe is authoritative — no point
				// holding the state at warn for several intervals.
				FailurePolicy: scraper.FailurePolicy{FailAfter: 1, RecoverAfter: 1},
			}},
			StateAggregation: &scraper.StateAggregationTask{
				Path: c.URLs.State,
			},
			Metrics: &scraper.MetricsTask{
				Paths: []string{c.URLs.Metrics},
			},
		}
		scfg.Targets = append(scfg.Targets, target)
	}

	sc, err := scraper.New(scfg)
	if err != nil {
		return nil, fmt.Errorf("build scraper: %w", err)
	}

	// Wire the scraper's outputs into the supervisor's bundle so they are
	// visible under the supervisor's /state and /metrics.
	for _, st := range sc.States() {
		if err := bundle.registry.Register(st); err != nil {
			return nil, fmt.Errorf("register scraper state %q: %w", st.Name(), err)
		}
	}
	if err := bundle.registry.RegisterCollectors(sc.MetricsCollector()); err != nil {
		return nil, fmt.Errorf("register scraper metrics: %w", err)
	}

	return &componentScraper{cfg: cfg, logger: logger, sc: sc}, nil
}

// Run starts the scraper's task loops and blocks until ctx is cancelled.
// Safe to call when no components are configured (no-op).
func (this *componentScraper) Run(ctx context.Context) {
	if this.sc == nil {
		<-ctx.Done()
		return
	}
	this.sc.Run(ctx)
}
