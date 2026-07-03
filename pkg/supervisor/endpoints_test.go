package supervisor

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("endpoint resolution", func() {
	DescribeTable("resolves endpoint specs",
		func(base, spec, wantBase, wantPath string) {
			got, err := resolveEndpoint(base, spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.BaseURL).To(Equal(wantBase))
			Expect(got.Path).To(Equal(wantPath))
		},
		Entry("absolute path", "http://localhost:8080", "/state", "http://localhost:8080", "/state"),
		Entry("bare path", "http://localhost:8080", "metrics", "http://localhost:8080", "/metrics"),
		Entry("custom port", "http://localhost:8080", ":9001/metrics", "http://localhost:9001", "/metrics"),
		Entry("custom port preserves scheme and hostname", "https://api.internal:8443", ":9001/state", "https://api.internal:9001", "/state"),
		Entry("absolute URL", "http://localhost:8080", "http://metrics.internal:9001/metrics?x=1", "http://metrics.internal:9001", "/metrics?x=1"),
	)

	It("rejects an empty custom port", func() {
		_, err := resolveEndpoint("http://localhost:8080", ":/state")
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("normalizes overflow paths",
		func(spec, want string) {
			got, err := normalizeOverflowPath(spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("absolute path", "/pprof/dump", "/pprof/dump"),
		Entry("bare path", "pprof/dump", "/pprof/dump"),
		Entry("query string", "/pprof/dump?kind=heap", "/pprof/dump?kind=heap"),
	)

	DescribeTable("rejects overflow paths with their own base",
		func(spec string) {
			_, err := normalizeOverflowPath(spec)
			Expect(err).To(HaveOccurred())
		},
		Entry("absolute URL", "http://localhost:8080/pprof/dump"),
		Entry("host-relative URL", "//localhost:8080/pprof/dump"),
		Entry("port override", ":8081/pprof/dump"),
	)
})

var _ = Describe("scrape target construction", func() {
	It("groups component scrape tasks by resolved base URL", func() {
		targets, err := scrapeTargetsFor("api", "http://localhost:8080", URLsConfig{
			Healthz: "/healthz",
			State:   ":8081/state",
			Metrics: ":9001/metrics",
		}, false, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(targets).To(HaveLen(3))

		byBase := map[string]bool{}
		for _, t := range targets {
			byBase[t.BaseURL] = true
			Expect(t.Name).To(Equal("api"))
		}
		Expect(byBase).To(HaveKey("http://localhost:8080"))
		Expect(byBase).To(HaveKey("http://localhost:8081"))
		Expect(byBase).To(HaveKey("http://localhost:9001"))
	})
})
