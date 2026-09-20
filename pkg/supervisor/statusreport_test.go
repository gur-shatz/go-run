package supervisor_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gur-shatz/go-run/pkg/supervisor"
	"github.com/gur-shatz/statekit"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("status reports", func() {
	var (
		dir       string
		collector *supervisor.StatusCollector
		server    *httptest.Server
		accepted  = map[string]string{"tok-a": "install-a"}
	)

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		collector = supervisor.NewStatusCollector(dir)
		server = httptest.NewServer(collector.Handler("test", func(r *http.Request) (string, bool) {
			id, ok := accepted[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			return id, ok
		}))
		DeferCleanup(server.Close)
	})

	report := func(components ...supervisor.ComponentSnapshot) supervisor.StatusReport {
		return supervisor.StatusReport{
			SentAt:          time.Now().UTC(),
			Hostname:        "host-a",
			Build:           supervisor.BuildInfo{Version: "v0.1.52"},
			IntervalSeconds: 60,
			Snapshot:        supervisor.SupervisorSnapshot{Components: components},
		}
	}

	It("stores what an authenticated reporter sends and refuses the rest", func() {
		rep := report(supervisor.ComponentSnapshot{Name: "gateway", Current: "v2", Status: "running", GlobalState: "pass"})
		Expect((&supervisor.StatusReporter{URL: server.URL, Bearer: "tok-a"}).Send(context.Background(), rep)).To(Succeed())

		got, ok := collector.Load("install-a")
		Expect(ok).To(BeTrue())
		Expect(got.Hostname).To(Equal("host-a"))
		Expect(got.Build.Version).To(Equal("v0.1.52"))
		Expect(got.Snapshot.Components).To(HaveLen(1))
		Expect(got.Snapshot.Components[0].Current).To(Equal("v2"))
		info, err := os.Stat(filepath.Join(dir, "install-a.status"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		err = (&supervisor.StatusReporter{URL: server.URL, Bearer: "nope"}).Send(context.Background(), rep)
		Expect(err).To(MatchError(ContainSubstring("401")))
		_, ok = collector.Load("nobody")
		Expect(ok).To(BeFalse())

		resp, err := http.Get(server.URL)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		Expect(collector.Remove("install-a")).To(Succeed())
		_, ok = collector.Load("install-a")
		Expect(ok).To(BeFalse())
		Expect(collector.Remove("install-a")).To(Succeed())
		Expect(collector.Store("../escape", rep)).To(MatchError(supervisor.ErrBadReporterID))
	})

	It("judges a report by freshness and its components", func() {
		now := time.Now()
		fresh := report(
			supervisor.ComponentSnapshot{Name: "backend", Status: "running", GlobalState: "pass"},
			supervisor.ComponentSnapshot{Name: "gateway", Status: "running", GlobalState: "pass"})
		v := supervisor.JudgeStatus(fresh, now)
		Expect(v.Status).To(Equal(statekit.Pass))
		Expect(v.Reason).To(Equal("2 components running"))
		Expect(v.Stale).To(BeFalse())

		warn := report(supervisor.ComponentSnapshot{Name: "gateway", Status: "running", GlobalState: "pass", UpdateState: "warn", UpdateReason: "target v3 is rejected; holding current"})
		v = supervisor.JudgeStatus(warn, now)
		Expect(v.Status).To(Equal(statekit.Warn))
		Expect(v.Reason).To(Equal("gateway target v3 is rejected; holding current"))

		fail := report(
			supervisor.ComponentSnapshot{Name: "backend", Status: "halted", StatusReason: "crash loop"},
			supervisor.ComponentSnapshot{Name: "gateway", Status: "running", GlobalState: "warn"})
		v = supervisor.JudgeStatus(fail, now)
		Expect(v.Status).To(Equal(statekit.Fail))
		Expect(v.Reason).To(Equal("backend halted"))

		pollErr := fresh
		pollErr.Snapshot.LastPollError = "dial tcp: refused"
		v = supervisor.JudgeStatus(pollErr, now)
		Expect(v.Status).To(Equal(statekit.Warn))
		Expect(v.Reason).To(Equal("poll: dial tcp: refused"))

		stale := fresh
		stale.SentAt = now.Add(-4 * time.Minute)
		v = supervisor.JudgeStatus(stale, now)
		Expect(v.Status).To(Equal(statekit.Down))
		Expect(v.Stale).To(BeTrue())
		Expect(v.Reason).To(HavePrefix("no report for 4m0s"))

		// Under the floor a short cadence is not stale after three misses.
		quick := fresh
		quick.IntervalSeconds, quick.SentAt = 5, now.Add(-time.Minute)
		Expect(supervisor.JudgeStatus(quick, now).Stale).To(BeFalse())
	})

	It("aggregates every reporter into one state", func() {
		Expect(collector.Store("a", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "running", GlobalState: "pass"}))).To(Succeed())
		Expect(collector.Store("b", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "halted"}))).To(Succeed())
		state := supervisor.NewReportsState(collector, func() []string { return []string{"b", "a", "c"} })
		Expect(state.Name()).To(Equal("reports"))
		snap := state.Snapshot()
		Expect(snap.Status).To(Equal(statekit.Fail))
		Expect(snap.Reason).To(Equal("3 reporters: 1 pass, 1 fail, 1 never reported"))
		Expect(snap.Data).To(HaveKey("c"))
		Expect(snap.Data["c"]).To(HaveKeyWithValue("level", "none"))
		Expect(snap.Data["b"]).To(HaveKeyWithValue("level", "fail"))
		Expect(snap.ChangedAt).NotTo(BeZero())
		Expect(collector.Store("b", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "running", GlobalState: "pass"}))).To(Succeed())
		later := state.Snapshot()
		Expect(later.Status).To(Equal(statekit.Pass))
		Expect(later.ChangedAt).To(BeTemporally(">=", snap.ChangedAt))
		Expect(state.Snapshot().ChangedAt).To(Equal(later.ChangedAt), "the same status keeps its change time")

		empty := supervisor.NewReportsState(collector, func() []string { return nil })
		Expect(empty.Snapshot().Status).To(Equal(statekit.Pass))
		Expect(empty.Snapshot().Reason).To(Equal("no reporters"))
	})

	It("stops for good when the origin does not collect reports", func() {
		declined := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(declined.Close)
		err := (&supervisor.StatusReporter{URL: declined.URL + "/status", Bearer: "tok-a"}).Send(context.Background(), report())
		Expect(err).To(MatchError(supervisor.ErrStatusNotCollected))

		// A 5xx or a refused connection is transient: the reporter keeps trying.
		flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
		DeferCleanup(flaky.Close)
		err = (&supervisor.StatusReporter{URL: flaky.URL + "/status", Bearer: "tok-a"}).Send(context.Background(), report())
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, supervisor.ErrStatusNotCollected)).To(BeFalse())
	})

	It("derives the report URL from the update origin", func() {
		Expect(supervisor.StatusReportURLForTest(supervisor.RemoteConfig{Enabled: true, BaseURL: "https://hub.example/versions"})).To(Equal("https://hub.example/versions/status"))
		Expect(supervisor.StatusReportURLForTest(supervisor.RemoteConfig{Enabled: true, BaseURL: "https://hub.example/versions/"})).To(Equal("https://hub.example/versions/status"))
		Expect(supervisor.StatusReportURLForTest(supervisor.RemoteConfig{Enabled: true, BaseURL: "file:///srv/origin"})).To(BeEmpty(), "a file origin collects nothing")
		Expect(supervisor.StatusReportURLForTest(supervisor.RemoteConfig{Enabled: false, BaseURL: "https://hub.example/versions"})).To(BeEmpty(), "updates off, nothing to report to")
	})
})
