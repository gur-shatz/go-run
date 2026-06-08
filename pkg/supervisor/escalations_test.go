package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/statekit"
	"github.com/gur-shatz/statekit/storage"

	"github.com/gur-shatz/go-run/internal/log"
)

var _ = Describe("supervisor incidents", func() {
	newCfg := func() Config {
		cfg := Config{StateDir: GinkgoT().TempDir()}
		cfg.Components = []ComponentConfig{{Name: "hello", Port: 18091, Command: "x", Remote: RemoteConfig{BaseURL: "file://x"}}}
		cfg.StateMonitor.Observe.Enabled = true
		cfg.ApplyDefaults()
		return cfg
	}

	It("opens a deployment incident on switch and closes it on stabilization", func() {
		bundle := newStatekitBundle(newCfg())
		bundle.incidentDeploy("hello", "v1", "v2")

		doc := bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(1))
		Expect(doc.Incidents[0].Type).To(Equal(statekit.EscalationTypeDeployment))
		Expect(doc.Incidents[0].Status).To(Equal(statekit.EscalationOpen))
		Expect(doc.Incidents[0].Title).To(ContainSubstring("deploy v1 → v2"))
		Expect(doc.Incidents[0].Topics).To(HaveKeyWithValue("component", "hello"))

		bundle.incidentStabilized("hello", "v2")
		doc = bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(1))
		Expect(doc.Incidents[0].Status).To(Equal(statekit.EscalationClosed))
	})

	It("folds repeated crashes into one incident and rolls over into a rollback incident", func() {
		bundle := newStatekitBundle(newCfg())
		bundle.incidentCrash("hello", "v2", "child exited after 1s", nil)
		bundle.incidentCrash("hello", "v2", "child exited after 800ms", nil)

		doc := bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(1))
		Expect(doc.Incidents[0].Type).To(Equal(incidentTypeCrash))
		Expect(doc.Incidents[0].Severity).To(Equal(statekit.Fail))
		// created + two crash logs
		Expect(doc.Incidents[0].Events).To(HaveLen(3))

		bundle.incidentRollback("hello", "v2", "v1", "fast crashes exceeded threshold")
		doc = bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(2))
		crash, rollback := doc.Incidents[0], doc.Incidents[1]
		Expect(crash.Status).To(Equal(statekit.EscalationClosed))
		Expect(rollback.Type).To(Equal(statekit.EscalationTypeRollback))
		Expect(rollback.Status).To(Equal(statekit.EscalationOpen))
		Expect(rollback.Title).To(ContainSubstring("rollback v2 → v1"))

		bundle.incidentStabilized("hello", "v1")
		doc = bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents[1].Status).To(Equal(statekit.EscalationClosed))
	})

	It("supersedes an open crash episode when a deploy switches versions", func() {
		bundle := newStatekitBundle(newCfg())
		bundle.incidentCrash("hello", "v2", "child exited after 1s", nil)
		bundle.incidentDeploy("hello", "v2", "v3")

		doc := bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(2))
		Expect(doc.Incidents[0].Status).To(Equal(statekit.EscalationClosed))
		Expect(doc.Incidents[1].Type).To(Equal(statekit.EscalationTypeDeployment))
		Expect(doc.Incidents[1].Status).To(Equal(statekit.EscalationOpen))
	})

	It("serves the incidents at /backoffice/escalations", func() {
		cfg := newCfg()
		bundle := newStatekitBundle(cfg)
		bundle.incidentDeploy("hello", "v1", "v2")
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18091}, nil, nil,
			NewPaths(cfg.StateDir), bundle, []ComponentConfig{{Name: "hello"}}, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := getBody(srv.Client(), srv.URL+"/backoffice/escalations")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("type: deployment"))
		Expect(body).To(ContainSubstring("deploy v1 → v2"))
	})

	It("mirrors incidents into the observer store attributed to their component", func() {
		cfg := newCfg()
		bundle := newStatekitBundle(cfg)
		obs := newObserver(cfg.StateMonitor.Observe, bundle.registry, log.New("[t]", false))
		bundle.incidentDeploy("hello", "v1", "v2")

		obs.ingestEscalations(context.Background())
		incidents, err := obs.store.Incidents(context.Background(), storage.IncidentFilter{Source: "hello"})
		Expect(err).NotTo(HaveOccurred())
		Expect(incidents).To(HaveLen(1))
		Expect(incidents[0].Type).To(Equal(statekit.EscalationTypeDeployment))
		Expect(incidents[0].Status).To(Equal(statekit.EscalationOpen))

		// Mirroring must not ack: the export cursor belongs to an upstream
		// fleet scraper, so the component-side document keeps its events.
		doc := bundle.registry.EscalationDisplay("", "")
		Expect(doc.Incidents).To(HaveLen(1))
		Expect(doc.Incidents[0].Events).NotTo(BeEmpty())
	})
})
