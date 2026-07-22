package supervisor

import "context"

func (this *Component) ComputeDesiredVersionForTest(ctx context.Context) (string, error) {
	return this.computeDesiredVersion(ctx)
}

// NewStatekitBundleForTest exposes the internal statekit bundle so black-box
// tests can assert lifecycle leaf severities via Component.Snapshot().
func NewStatekitBundleForTest(cfg Config) *statekitBundle {
	return newStatekitBundle(cfg)
}
