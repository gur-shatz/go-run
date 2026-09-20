package supervisor

// statusreport.go is the supervisor's status reporting and the server side
// of it. A supervisor updating from an HTTP origin POSTs a StatusReport
// (its own snapshot: what every component runs and how it fares) to
// <remote.base_url>/status after each poll cycle, under the update
// channel's bearer; an origin that answers 404 or 405 does not collect
// reports and is never asked again. Any origin server can collect them:
// StatusCollector stores the last report per reporter behind the server's
// own authentication, JudgeStatus reads one (fresh or stale by the cadence
// the report declares, healthy or not by its components), and ReportsState
// is a statekit view over all of them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gur-shatz/statekit"
)

// StatusReport is one supervisor's state at one moment: its snapshot plus
// what the receiver needs to place it (host, build, cadence).
type StatusReport struct {
	SentAt   time.Time `json:"sent_at"`
	Hostname string    `json:"hostname,omitempty"`
	Build    BuildInfo `json:"build"`
	// IntervalSeconds is the poll cadence: a report is stale after a few
	// of them pass without a successor.
	IntervalSeconds int64              `json:"interval_seconds"`
	Snapshot        SupervisorSnapshot `json:"snapshot"`
}

// MaxStatusReportBytes bounds a report body on the receiving side.
const MaxStatusReportBytes = 256 << 10

// StatusReportPath is where reports go, under the update origin's base URL.
const StatusReportPath = "status"

// ErrStatusNotCollected is Send's answer to a 404 or 405: the origin does
// not collect status reports, so there is no point in sending more.
var ErrStatusNotCollected = errors.New("origin does not collect status reports")

// statusReportURL is the report URL for a remote, "" when the remote is off
// or not HTTP (a file:// origin collects nothing).
func statusReportURL(remote RemoteConfig) string {
	if !remote.Enabled || !strings.HasPrefix(remote.BaseURL, "http") {
		return ""
	}
	u, err := joinURL(remote.BaseURL, StatusReportPath)
	if err != nil {
		return ""
	}
	return u
}

// StatusReporter posts reports to one URL under one bearer.
type StatusReporter struct {
	URL    string
	Bearer string
	Client *http.Client
}

// Send posts the report; any answer but 2xx is an error carrying the
// status and the first line of the body.
func (this *StatusReporter) Send(ctx context.Context, rep StatusReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, this.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if this.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+this.Bearer)
	}
	client := this.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("%s: %s: %w", this.URL, resp.Status, ErrStatusNotCollected)
	}
	if resp.StatusCode/100 != 2 {
		line, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		first, _, _ := strings.Cut(strings.TrimSpace(string(line)), "\n")
		return fmt.Errorf("%s: %s %s", this.URL, resp.Status, first)
	}
	return nil
}

// buildStatusReport is the report for this supervisor now.
func (this *Supervisor) buildStatusReport() StatusReport {
	host, _ := os.Hostname()
	return StatusReport{
		SentAt:          time.Now().UTC(),
		Hostname:        host,
		Build:           this.buildInfo,
		IntervalSeconds: int64(this.cfg.Remote.PollingInterval / time.Second),
		Snapshot:        this.Snapshot(),
	}
}

// runStatusReporter sends a report shortly after start and then once per
// poll interval, so the receiver sees each poll's outcome, and stops for
// good once the origin says it does not collect them. Failures are logged
// on change only; the "status.report" state carries the verdict.
func (this *Supervisor) runStatusReporter(ctx context.Context, url string) {
	interval := this.cfg.Remote.PollingInterval
	if interval <= 0 {
		interval = time.Minute
	}
	reporter := &StatusReporter{URL: url, Bearer: this.cfg.Remote.Secret}
	state := statekit.NewManualState("status.report", statekit.WithImportance(statekit.Informational),
		statekit.WithHelp("The last status report to "+reporter.URL))
	state.Warn("no report sent yet", nil)
	_ = this.bundle.registry.Register(&taggedState{underlying: state, scrapedFrom: "supervisor"})

	failing := false
	// send reports once; false when the origin declined and the loop is over.
	send := func() bool {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		err := reporter.Send(sendCtx, this.buildStatusReport())
		if errors.Is(err, ErrStatusNotCollected) {
			state.Pass("not collected by the origin; reporting stopped", nil)
			this.logger.Status("status reports: %v; reporting stopped", err)
			return false
		}
		if err != nil {
			state.Warn(err.Error(), nil)
			if !failing {
				this.logger.Warn("status report to %s failed: %v", reporter.URL, err)
			}
			failing = true
			return true
		}
		state.Pass("reported", nil)
		if failing {
			this.logger.Status("status report to %s succeeded again", reporter.URL)
		}
		failing = false
		return true
	}

	// The first report waits for the components' first poll to land.
	first := time.NewTimer(min(interval/4, 15*time.Second))
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
		if !send() {
			return
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !send() {
				return
			}
		}
	}
}

// reporterIDRE bounds a reporter id to a safe file name.
var reporterIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ErrBadReporterID is returned for an id the collector will not name a file after.
var ErrBadReporterID = errors.New("invalid reporter id")

// StatusCollector keeps the last report of every reporter as
// <dir>/<id>.status. The id is whatever the server's authentication names.
// With Stages set, every stored report also marks what each component
// runs on the reporter's stage record.
type StatusCollector struct {
	dir    string
	Stages *StageStore
}

func NewStatusCollector(dir string) *StatusCollector {
	return &StatusCollector{dir: dir}
}

func (this *StatusCollector) file(id string) (string, error) {
	if !reporterIDRE.MatchString(id) {
		return "", ErrBadReporterID
	}
	return filepath.Join(this.dir, id+".status"), nil
}

// Store replaces the reporter's report.
func (this *StatusCollector) Store(id string, rep StatusReport) error {
	p, err := this.file(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(this.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	if this.Stages != nil {
		for _, c := range rep.Snapshot.Components {
			_ = this.Stages.Running(id, c.Name, c.Current, c.Status, rep.SentAt)
		}
	}
	return nil
}

// Load is the reporter's last report, if any.
func (this *StatusCollector) Load(id string) (StatusReport, bool) {
	p, err := this.file(id)
	if err != nil {
		return StatusReport{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return StatusReport{}, false
	}
	var rep StatusReport
	if json.Unmarshal(data, &rep) != nil {
		return StatusReport{}, false
	}
	return rep, true
}

// Remove forgets the reporter's report; no report is no error.
func (this *StatusCollector) Remove(id string) error {
	p, err := this.file(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Handler accepts POSTed reports. authenticate names the reporter a request
// speaks for; a request it refuses gets 401 with the realm.
func (this *StatusCollector) Handler(realm string, authenticate func(*http.Request) (id string, ok bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var rep StatusReport
		if err := json.NewDecoder(io.LimitReader(r.Body, MaxStatusReportBytes)).Decode(&rep); err != nil {
			http.Error(w, "bad report: "+err.Error(), http.StatusBadRequest)
			return
		}
		if rep.SentAt.IsZero() {
			rep.SentAt = time.Now().UTC()
		}
		if err := this.Store(id, rep); err != nil {
			http.Error(w, "cannot store report", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// StatusVerdict is the receiver's reading of one report at one moment.
type StatusVerdict struct {
	Status statekit.Status `json:"-"`
	// Level is Status as text: pass, warn, fail, down.
	Level  string `json:"level"`
	Reason string `json:"reason"`
	// AgeSeconds is how long ago the report was sent.
	AgeSeconds int64 `json:"age_seconds"`
	Stale      bool  `json:"stale"`
}

// A report is stale after this many poll intervals without a successor,
// never sooner than the floor.
const (
	statusStaleAfter    = 3
	statusStaleAfterMin = 2 * time.Minute
)

// JudgeStatus reads a report: stale is down; else the worst of its
// components by the supervisor's own view, run and update: a lifecycle at
// fail or down (halted, not started) fails the reporter, one at warn
// (restarting), a failed poll, or an update warning warns it. What a component says of its own health is
// its business, not the origin's. The reason names every trip.
func JudgeStatus(rep StatusReport, now time.Time) StatusVerdict {
	age := now.Sub(rep.SentAt)
	limit := time.Duration(rep.IntervalSeconds) * time.Second * statusStaleAfter
	if limit < statusStaleAfterMin {
		limit = statusStaleAfterMin
	}
	v := StatusVerdict{AgeSeconds: int64(age.Seconds())}
	if age > limit {
		v.Status, v.Stale = statekit.Down, true
		v.Reason = fmt.Sprintf("no report for %s", age.Truncate(time.Second))
		v.Level = v.Status.String()
		return v
	}
	var fails, warns []string
	for _, c := range rep.Snapshot.Components {
		// Status is the lifecycle leaf's statekit status; the reason says
		// what the process does (running, restarting, halted, not started).
		switch c.Status {
		case "fail", "down":
			fails = append(fails, c.Name+" "+firstNonEmpty(c.StatusReason, c.Status))
		case "warn":
			warns = append(warns, c.Name+" "+firstNonEmpty(c.StatusReason, c.Status))
		}
		if c.UpdateState == "warn" || c.UpdateState == "fail" {
			warns = append(warns, c.Name+" update: "+firstNonEmpty(c.UpdateReason, c.UpdateState))
		}
	}
	if rep.Snapshot.LastPollError != "" {
		warns = append(warns, "poll: "+rep.Snapshot.LastPollError)
	}
	switch {
	case len(fails) > 0:
		v.Status, v.Reason = statekit.Fail, strings.Join(fails, "; ")
	case len(warns) > 0:
		v.Status, v.Reason = statekit.Warn, strings.Join(warns, "; ")
	default:
		v.Status, v.Reason = statekit.Pass, fmt.Sprintf("%d components running", len(rep.Snapshot.Components))
	}
	v.Level = v.Status.String()
	return v
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ReportsState is a statekit view over the reports of the reporters a
// lister names: the worst verdict wins, every reporter is in Data. A
// reporter without a report counts as never reported.
type ReportsState struct {
	collector *StatusCollector
	list      func() []string

	mu        sync.Mutex
	last      statekit.Status
	changedAt time.Time
}

func NewReportsState(c *StatusCollector, list func() []string) *ReportsState {
	return &ReportsState{collector: c, list: list}
}

func (this *ReportsState) Name() string { return "reports" }

func (this *ReportsState) Snapshot() statekit.Snapshot {
	now := time.Now()
	ids := this.list()
	sort.Strings(ids)
	snap := statekit.Snapshot{
		Name:       this.Name(),
		Status:     statekit.Pass,
		Importance: statekit.Important,
		Help:       "Every reporter's last status report, judged by freshness and its components' health.",
		UpdatedAt:  now,
		Data:       make(map[string]any, len(ids)),
	}
	counts := map[string]int{}
	for _, id := range ids {
		rep, ok := this.collector.Load(id)
		if !ok {
			snap.Data[id] = map[string]any{"level": "none", "reason": "never reported"}
			counts["never reported"]++
			continue
		}
		v := JudgeStatus(rep, now)
		snap.Data[id] = map[string]any{"level": v.Level, "reason": v.Reason, "age_seconds": v.AgeSeconds, "build": rep.Build.Version}
		counts[v.Level]++
		if v.Status > snap.Status {
			snap.Status = v.Status
		}
	}
	if len(ids) == 0 {
		snap.Reason = "no reporters"
	} else {
		parts := make([]string, 0, len(counts))
		for _, level := range []string{"pass", "warn", "fail", "down", "never reported"} {
			if n := counts[level]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, level))
			}
		}
		snap.Reason = fmt.Sprintf("%d reporters: %s", len(ids), strings.Join(parts, ", "))
	}
	snap.ChangedAt = this.changed(snap.Status, now)
	return snap
}

// changed is when the aggregate status last moved: the first snapshot
// starts the clock.
func (this *ReportsState) changed(status statekit.Status, now time.Time) time.Time {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.changedAt.IsZero() || status != this.last {
		this.last, this.changedAt = status, now
	}
	return this.changedAt
}
