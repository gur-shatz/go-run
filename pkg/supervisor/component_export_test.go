package supervisor

import "context"

func (this *Component) ComputeDesiredVersionForTest(ctx context.Context) (string, error) {
	return this.computeDesiredVersion(ctx)
}
