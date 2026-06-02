package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("component global-state probe", func() {
	probeAgainst := func(code int, body string) string {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
		defer backend.Close()
		port, _ := strconv.Atoi(must(url.Parse(backend.URL)).Port())
		c := &Component{cfg: ComponentConfig{Port: port, URLs: URLsConfig{Healthz: "/healthz"}}}
		return c.probeGlobalState(context.Background())
	}

	DescribeTable("maps /healthz to the global-state vocabulary",
		func(code int, body, want string) { Expect(probeAgainst(code, body)).To(Equal(want)) },
		Entry("200 pass", 200, "pass\n", "pass"),
		Entry("200 warn", 200, "warn", "warn"),
		Entry("200 fail", 200, "fail\n", "fail"),
		Entry("200 unrecognised body is alive -> pass", 200, "ok", "pass"),
		Entry("500 -> down", 500, "fail", "down"),
		Entry("404 -> down", 404, "", "down"),
	)

	It("reports down when nothing is listening", func() {
		c := &Component{cfg: ComponentConfig{Port: 1, URLs: URLsConfig{Healthz: "/healthz"}}}
		Expect(c.probeGlobalState(context.Background())).To(Equal("down"))
	})
})

var _ = Describe("component update-status", func() {
	statusFor := func(o ForcedOverride) string {
		c := &Component{getForced: func() ForcedOverride { return o }}
		return c.updateStatus()
	}

	It("is live with no override", func() {
		Expect(statusFor(ForcedOverride{Kind: ForcedKindNone})).To(Equal("live"))
	})
	It("is pinned to stable when forced to stable", func() {
		Expect(statusFor(ForcedOverride{Kind: ForcedKindStable})).To(Equal("pinned to stable"))
	})
	It("is pinned to a version when forced to one", func() {
		Expect(statusFor(ForcedOverride{Kind: ForcedKindVersion, Version: "v9"})).To(Equal("pinned to v9"))
	})
})
