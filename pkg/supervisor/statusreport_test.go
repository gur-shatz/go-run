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
		rep := report(supervisor.ComponentSnapshot{Name: "gateway", Current: "v2", Status: "pass", GlobalState: "pass"})
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
			supervisor.ComponentSnapshot{Name: "backend", Status: "pass", StatusReason: "running v1 (pid=41)", GlobalState: "pass"},
			supervisor.ComponentSnapshot{Name: "gateway", Status: "pass", StatusReason: "running v1 (pid=42)", GlobalState: "pass"})
		v := supervisor.JudgeStatus(fresh, now)
		Expect(v.Status).To(Equal(statekit.Pass))
		Expect(v.Reason).To(Equal("2 components running"))
		Expect(v.Stale).To(BeFalse())

		warn := report(supervisor.ComponentSnapshot{Name: "gateway", Status: "pass", GlobalState: "pass", UpdateState: "warn", UpdateReason: "target v3 is rejected; holding current"})
		v = supervisor.JudgeStatus(warn, now)
		Expect(v.Status).To(Equal(statekit.Warn))
		Expect(v.Reason).To(Equal("gateway update: target v3 is rejected; holding current"))

		health := report(supervisor.ComponentSnapshot{Name: "backend", Status: "pass", GlobalState: "warn", UpdateState: "pass", UpdateReason: "prepared v1"})
		v = supervisor.JudgeStatus(health, now)
		Expect(v.Status).To(Equal(statekit.Pass), "a component's own health is not the origin's concern")

		both := report(supervisor.ComponentSnapshot{Name: "backend", Status: "warn", StatusReason: "restarting (exit 1)", GlobalState: "fail", UpdateState: "warn", UpdateReason: "holding"})
		Expect(supervisor.JudgeStatus(both, now).Reason).To(Equal("backend restarting (exit 1); backend update: holding"))

		down := report(supervisor.ComponentSnapshot{Name: "gateway", Status: "down", StatusReason: "not started"})
		v = supervisor.JudgeStatus(down, now)
		Expect(v.Status).To(Equal(statekit.Fail))
		Expect(v.Reason).To(Equal("gateway not started"))

		fail := report(
			supervisor.ComponentSnapshot{Name: "backend", Status: "fail", StatusReason: "halted: crash loop"},
			supervisor.ComponentSnapshot{Name: "gateway", Status: "pass", GlobalState: "fail"})
		v = supervisor.JudgeStatus(fail, now)
		Expect(v.Status).To(Equal(statekit.Fail))
		Expect(v.Reason).To(Equal("backend halted: crash loop"))

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
		Expect(collector.Store("a", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "pass", GlobalState: "pass"}))).To(Succeed())
		Expect(collector.Store("b", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "fail", StatusReason: "halted: crash loop"}))).To(Succeed())
		state := supervisor.NewReportsState(collector, func() []string { return []string{"b", "a", "c"} })
		Expect(state.Name()).To(Equal("reports"))
		snap := state.Snapshot()
		Expect(snap.Status).To(Equal(statekit.Fail))
		Expect(snap.Reason).To(Equal("3 reporters: 1 pass, 1 fail, 1 never reported"))
		Expect(snap.Data).To(HaveKey("c"))
		Expect(snap.Data["c"]).To(HaveKeyWithValue("level", "none"))
		Expect(snap.Data["b"]).To(HaveKeyWithValue("level", "fail"))
		Expect(snap.ChangedAt).NotTo(BeZero())
		Expect(collector.Store("b", report(supervisor.ComponentSnapshot{Name: "gateway", Status: "pass", GlobalState: "pass"}))).To(Succeed())
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

var _ = Describe("stages", func() {
	var store *supervisor.StageStore

	BeforeEach(func() {
		store = supervisor.NewStageStore(GinkgoT().TempDir())
	})

	It("keeps one record per reporter with each component's three stages", func() {
		Expect(store.Load("a").Components).To(BeEmpty())
		Expect(store.Requested("a", "gateway", "v1")).To(Succeed())
		Expect(store.Downloaded("a", "gateway", "v1")).To(Succeed())
		Expect(store.Requested("a", "backend", "")).To(Succeed())
		at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
		Expect(store.Running("a", "gateway", "v1", "pass", at)).To(Succeed())

		s := store.Load("a")
		Expect(s.Names()).To(Equal([]string{"backend", "gateway"}))
		gw := s.Components["gateway"]
		Expect(gw.Requested.Version).To(Equal("v1"))
		Expect(gw.Downloaded.Version).To(Equal("v1"))
		Expect(gw.Running).To(Equal(supervisor.Stage{Version: "v1", At: at, Note: "pass"}))
		Expect(gw.Summary()).To(Equal("running v1"))
		be := s.Components["backend"]
		Expect(be.Requested.Note).To(Equal("nothing to run"))
		Expect(be.Summary()).To(Equal("asked, nothing to run"))

		Expect(store.Requested("a", "gateway", "v2")).To(Succeed())
		Expect(store.Load("a").Components["gateway"].Summary()).To(Equal("told v2, running v1"))
		Expect(store.Downloaded("a", "gateway", "v2")).To(Succeed())
		Expect(store.Load("a").Components["gateway"].Summary()).To(Equal("downloaded v2, still running v1"))
		Expect(store.Running("a", "gateway", "v2", "pass", at.Add(time.Minute))).To(Succeed())
		Expect(store.Load("a").Components["gateway"].Summary()).To(Equal("running v2"))

		Expect((supervisor.ComponentStages{}).Summary()).To(Equal("no contact"))
		Expect((supervisor.ComponentStages{Requested: supervisor.Stage{Version: "v1", At: at}}).Summary()).To(Equal("told v1, not downloaded"))
		Expect((supervisor.ComponentStages{Requested: supervisor.Stage{Version: "v1", At: at}, Downloaded: supervisor.Stage{Version: "v1", At: at}}).Summary()).To(Equal("downloaded v1, not running yet"))
		Expect((supervisor.ComponentStages{Running: supervisor.Stage{Version: "v1", At: at}}).Summary()).To(Equal("running v1, never asked"))

		Expect(store.Requested("../x", "gateway", "v1")).To(MatchError(supervisor.ErrBadReporterID))
		Expect(store.Remove("a")).To(Succeed())
		Expect(store.Load("a").Components).To(BeEmpty())
		Expect(store.Remove("a")).To(Succeed())
	})

	It("is fed by the collector with what each component runs and its run state", func() {
		collector := supervisor.NewStatusCollector(GinkgoT().TempDir())
		collector.Stages = store
		at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
		rep := supervisor.StatusReport{SentAt: at, IntervalSeconds: 60, Snapshot: supervisor.SupervisorSnapshot{Components: []supervisor.ComponentSnapshot{
			{Name: "gateway", Current: "v1", Status: "pass", StatusReason: "running v1 (pid=7)", GlobalState: "pass"},
			{Name: "backend", Status: "down", StatusReason: "not started"}}}}
		Expect(collector.Store("a", rep)).To(Succeed())
		s := store.Load("a")
		Expect(s.Components["gateway"].Running).To(Equal(supervisor.Stage{Version: "v1", At: at, Note: "pass"}))
		Expect(s.Components["backend"].Running).To(Equal(supervisor.Stage{At: at, Note: "down"}))
	})
})
