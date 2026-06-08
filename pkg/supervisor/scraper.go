package supervisor

import (
	"context"
	"fmt"

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
//
// ingestor is optional (the observer's store when observe is enabled). When
// present, each component's own /escalations endpoint is scraped too, so
// statekit-based children can surface their support incidents on the /health
// console alongside the supervisor's lifecycle incidents.
func newComponentScraper(cfg Config, bundle *statekitBundle, ingestor scraper.EscalationIngestor, logger *log.Logger) (*componentScraper, error) {
	if len(cfg.Components) == 0 && len(cfg.ExternalComponents) == 0 {
		return &componentScraper{cfg: cfg, logger: logger}, nil
	}

	scfg := scraper.Config{
		Defaults: scraper.Defaults{
			Interval:   scraper.Duration(cfg.StateMonitor.Scrape.Interval),
			Timeout:    scraper.Duration(cfg.StateMonitor.Scrape.Timeout),
			Expiration: scraper.Duration(cfg.StateMonitor.Scrape.Expiration),
		},
	}

	for _, c := range cfg.Components {
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", c.Port)
		targets, err := scrapeTargetsFor(c.Name, baseURL, c.URLs, true, ingestor != nil)
		if err != nil {
			return nil, fmt.Errorf("component %q scrape targets: %w", c.Name, err)
		}
		scfg.Targets = append(scfg.Targets, targets...)
	}
	for _, c := range cfg.ExternalComponents {
		targets, err := scrapeTargetsFor(c.Name, c.URL, c.URLs, false, ingestor != nil)
		if err != nil {
			return nil, fmt.Errorf("external component %q scrape targets: %w", c.Name, err)
		}
		scfg.Targets = append(scfg.Targets, targets...)
	}

	var opts []scraper.Option
	if ingestor != nil {
		opts = append(opts, scraper.WithEscalationIngestor(ingestor))
	}
	sc, err := scraper.New(scfg, opts...)
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

func scrapeTargetsFor(name, baseURL string, urls URLsConfig, local bool, includeEscalations bool) ([]scraper.TargetConfig, error) {
	byBase := map[string]*scraper.TargetConfig{}
	targetFor := func(endpoint resolvedEndpoint) *scraper.TargetConfig {
		target := byBase[endpoint.BaseURL]
		if target == nil {
			target = &scraper.TargetConfig{
				Name:    name,
				BaseURL: endpoint.BaseURL,
			}
			byBase[endpoint.BaseURL] = target
		}
		return target
	}

	if urls.Healthz != "" {
		ep, err := resolveEndpoint(baseURL, urls.Healthz)
		if err != nil {
			return nil, fmt.Errorf("healthz: %w", err)
		}
		target := targetFor(ep)
		policy := scraper.FailurePolicy{FailAfter: 1, RecoverAfter: 1}
		if !local {
			policy = scraper.FailurePolicy{}
		}
		target.Liveness = append(target.Liveness, scraper.LivenessTask{
			ID:            "responsive",
			Path:          ep.Path,
			ExpectStatus:  []int{200},
			FailurePolicy: policy,
		})
	}
	if urls.State != "" {
		ep, err := resolveEndpoint(baseURL, urls.State)
		if err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
		targetFor(ep).StateAggregation = &scraper.StateAggregationTask{Path: ep.Path}
	}
	if urls.Metrics != "" {
		ep, err := resolveEndpoint(baseURL, urls.Metrics)
		if err != nil {
			return nil, fmt.Errorf("metrics: %w", err)
		}
		target := targetFor(ep)
		if target.Metrics == nil {
			target.Metrics = &scraper.MetricsTask{}
		}
		target.Metrics.Paths = append(target.Metrics.Paths, ep.Path)
	}
	if includeEscalations && urls.Escalations != "" {
		ep, err := resolveEndpoint(baseURL, urls.Escalations)
		if err != nil {
			return nil, fmt.Errorf("escalations: %w", err)
		}
		targetFor(ep).Escalations = &scraper.EscalationsTask{Path: ep.Path}
	}

	out := make([]scraper.TargetConfig, 0, len(byBase))
	for _, target := range byBase {
		if len(byBase) == 1 {
			target.ID = name
		}
		out = append(out, *target)
	}
	return out, nil
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
