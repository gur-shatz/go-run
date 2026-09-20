package supervisor

// stages.go is an origin's view of where each reporter stands: per
// component, the version it was last told to run (requested), the bundle
// it last fetched (downloaded), and what its report says it runs
// (running), each with a time. One record per reporter, current state
// only: the question it answers is "did the release land there?", not
// "what happened when".

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Stage is one step reached for one component: which version, when, and
// a note (the run state for running, "nothing to run" for a request the
// origin could not answer).
type Stage struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Note    string    `json:"note,omitempty"`
}

// ComponentStages is one component's three stages; a zero stage was not
// reached.
type ComponentStages struct {
	Requested  Stage `json:"requested"`
	Downloaded Stage `json:"downloaded"`
	Running    Stage `json:"running"`
}

// Summary says where the component stands in one line: what it runs
// against what it was told to run.
func (this ComponentStages) Summary() string {
	req, dl, run := this.Requested, this.Downloaded, this.Running
	switch {
	case req.At.IsZero() && run.At.IsZero():
		return "no contact"
	case req.At.IsZero():
		return "running " + run.Version + ", never asked"
	case req.Version == "":
		return "asked, nothing to run"
	case run.Version == req.Version:
		return "running " + req.Version
	case dl.Version == req.Version && run.At.IsZero():
		return "downloaded " + req.Version + ", not running yet"
	case dl.Version == req.Version:
		return "downloaded " + req.Version + ", still running " + run.Version
	case run.At.IsZero():
		return "told " + req.Version + ", not downloaded"
	default:
		return "told " + req.Version + ", running " + run.Version
	}
}

// Stages is one reporter's record: its components by name.
type Stages struct {
	Components map[string]*ComponentStages `json:"components"`
}

func (this *Stages) component(name string) *ComponentStages {
	if this.Components == nil {
		this.Components = map[string]*ComponentStages{}
	}
	c, ok := this.Components[name]
	if !ok {
		c = &ComponentStages{}
		this.Components[name] = c
	}
	return c
}

// Names lists the components on record, sorted.
func (this Stages) Names() []string {
	names := make([]string, 0, len(this.Components))
	for n := range this.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// StageStore keeps <dir>/<id>.stages, one Stages per reporter.
type StageStore struct {
	dir string
	mu  sync.Mutex
}

func NewStageStore(dir string) *StageStore {
	return &StageStore{dir: dir}
}

func (this *StageStore) file(id string) (string, error) {
	if !reporterIDRE.MatchString(id) {
		return "", ErrBadReporterID
	}
	return filepath.Join(this.dir, id+".stages"), nil
}

// Load is the reporter's record; empty when it has none.
func (this *StageStore) Load(id string) Stages {
	p, err := this.file(id)
	if err != nil {
		return Stages{}
	}
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.read(p)
}

// Requested notes what the origin told a component to run now; version
// "" is a request it could not answer.
func (this *StageStore) Requested(id, component, version string) error {
	return this.update(id, func(s *Stages) {
		c := s.component(component)
		c.Requested = Stage{Version: version, At: time.Now().UTC()}
		if version == "" {
			c.Requested.Note = "nothing to run"
		}
	})
}

// Downloaded notes the bundle a component fetched now.
func (this *StageStore) Downloaded(id, component, version string) error {
	return this.update(id, func(s *Stages) {
		s.component(component).Downloaded = Stage{Version: version, At: time.Now().UTC()}
	})
}

// Running notes what a report says a component runs, with its health.
func (this *StageStore) Running(id, component, version, health string, at time.Time) error {
	return this.update(id, func(s *Stages) {
		s.component(component).Running = Stage{Version: version, At: at, Note: health}
	})
}

// Remove forgets the reporter's record; none is no error.
func (this *StageStore) Remove(id string) error {
	p, err := this.file(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (this *StageStore) update(id string, apply func(*Stages)) error {
	p, err := this.file(id)
	if err != nil {
		return err
	}
	this.mu.Lock()
	defer this.mu.Unlock()
	s := this.read(p)
	apply(&s)
	return this.write(p, s)
}

func (this *StageStore) read(p string) Stages {
	var s Stages
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func (this *StageStore) write(p string, s Stages) error {
	if err := os.MkdirAll(this.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
