package supervisordeploy

import "testing"

// TestPackageNameRE locks in that a version containing underscores (the default
// 260622_224159_master stamp) is captured whole, rather than being eaten by a
// greedy component group. Component names must not contain '_'.
func TestPackageNameRE(t *testing.T) {
	cases := []struct {
		name              string
		comp, ver, platfm string
	}{
		{"backend_260622_224159_master_linux_arm64.tar.gz", "backend", "260622_224159_master", "linux_arm64"},
		{"backend_260622-master_linux_arm64.tar.gz", "backend", "260622-master", "linux_arm64"},
		{"backend_260609-analytics_linux_arm64.tar.gz", "backend", "260609-analytics", "linux_arm64"},
		{"frontend_260606_001610_master_linux_arm64.tar.gz", "frontend", "260606_001610_master", "linux_arm64"},
		{"gateway_260605_135232_master_darwin_amd64.tar.gz", "gateway", "260605_135232_master", "darwin_amd64"},
	}
	for _, c := range cases {
		m := packageNameRE.FindStringSubmatch(c.name)
		if m == nil {
			t.Errorf("%s: no match", c.name)
			continue
		}
		if m[1] != c.comp || m[2] != c.ver || m[3] != c.platfm {
			t.Errorf("%s: got comp=%q ver=%q plat=%q; want comp=%q ver=%q plat=%q",
				c.name, m[1], m[2], m[3], c.comp, c.ver, c.platfm)
		}
	}
}
