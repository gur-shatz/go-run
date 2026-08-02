package supervisor

import (
	"context"

	"github.com/gur-shatz/statekit"
)

// VersionGCStateForTest exposes the versions.gc leaf so black-box tests can
// assert what a sweep recorded without going through the HTTP surface.
func (this *Supervisor) VersionGCStateForTest() statekit.Snapshot {
	return this.bundle.versionGCLive.Snapshot()
}

// SweepOrphanVersionsForTest runs one orphan sweep on demand.
func (this *Supervisor) SweepOrphanVersionsForTest() {
	this.sweepOrphanVersions()
}

func (this *Component) ComputeDesiredVersionForTest(ctx context.Context) (string, error) {
	return this.computeDesiredVersion(ctx)
}

// NewStatekitBundleForTest exposes the internal statekit bundle so black-box
// tests can assert lifecycle leaf severities via Component.Snapshot().
func NewStatekitBundleForTest(cfg Config) *statekitBundle {
	return newStatekitBundle(cfg)
}
