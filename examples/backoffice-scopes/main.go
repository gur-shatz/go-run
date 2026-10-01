// backoffice-scopes is the live reference for chiutil item scopes: an
// ObjectsFolder whose items are folders, with scoped ObjectsFolders,
// WildcardFolders and plain folders mounted per item, two levels deep.
//
//	/backoffice/orgs/                                   ObjectsFolder (param orgId)
//	/backoffice/orgs/{orgId}/                           item scope
//	  billing/                                          plain folder, reads the org
//	  regions/{region}/                                 WildcardFolder, reads the org
//	  members/{id}/                                     scoped ObjectsFolder, FlatJSON
//	  projects/{id}/                                    scoped ObjectsFolder
//	    deployments/{id}/                               scoped again, same param name
//	    environments/{env}/                             WildcardFolder, reads org + project
//	/backoffice/services/{name}/                        top-level WildcardFolder
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

type Org struct {
	ID       string
	Name     string
	Plan     string
	Members  []*Member
	Projects []*Project
}

type Member struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type Project struct {
	ID          string
	Repo        string
	Deployments []*Deployment
}

type Deployment struct {
	ID      string
	Version string
	Status  string
}

var orgs = map[string]*Org{
	"acme": {
		ID: "acme", Name: "Acme Corp", Plan: "enterprise",
		Members: []*Member{
			{ID: "alice", Email: "alice@acme.test", Role: "owner"},
			{ID: "bob", Email: "bob@acme.test", Role: "developer"},
		},
		Projects: []*Project{
			{ID: "api", Repo: "acme/api", Deployments: []*Deployment{
				{ID: "d-101", Version: "v1.4.0", Status: "live"},
				{ID: "d-100", Version: "v1.3.2", Status: "superseded"},
			}},
			{ID: "web", Repo: "acme/web", Deployments: []*Deployment{
				{ID: "d-201", Version: "v0.9.1", Status: "failed"},
			}},
		},
	},
	"globex": {
		ID: "globex", Name: "Globex Inc", Plan: "team",
		Members: []*Member{{ID: "carol", Email: "carol@globex.test", Role: "owner"}},
		Projects: []*Project{
			{ID: "billing", Repo: "globex/billing", Deployments: []*Deployment{
				{ID: "d-301", Version: "v2.0.0", Status: "live"},
			}},
		},
	},
}

func main() {
	port := os.Getenv("SCOPES_PORT")
	if port == "" {
		port = "18084"
	}

	router := chi.NewRouter()
	bo := chiutil.NewRouteFolder(router, "/backoffice").ServiceName("Scopes demo").Title("Backoffice")

	// Level 1: organizations. Each org is a folder (its Scope).
	orgsFolder := chiutil.ObjectsFolder(bo, "orgs", orgMapper{}, chiutil.WithParam("orgId")).
		Title("Organizations").
		Description("Every org is a folder: routes, sub-collections and wildcard folders per org")
	org := orgsFolder.Scope()

	// A plain folder: its handlers read the org from the request.
	billing := org.Folder("billing").Description("Plan and invoices of this org")
	billing.GetDesc("/plan", "Current plan", func(w http.ResponseWriter, r *http.Request) {
		o, _ := chiutil.ItemOf[*Org](r)
		writeJSON(w, map[string]string{"org": o.ID, "plan": o.Plan})
	})
	billing.GetDesc("/invoices", "Invoices", func(w http.ResponseWriter, r *http.Request) {
		o, _ := chiutil.ItemOf[*Org](r)
		writeJSON(w, []map[string]string{
			{"org": o.ID, "invoice": "INV-" + o.ID + "-2026-09", "amount": "1200.00"},
			{"org": o.ID, "invoice": "INV-" + o.ID + "-2026-08", "amount": "1150.00"},
		})
	})

	// A handler that finds nothing answers with chiutil.NotFound: browsers get
	// the 404 page leading back to billing/.
	billing.GetDesc("/contract", "Signed contract (enterprise plans only)", func(w http.ResponseWriter, r *http.Request) {
		o, _ := chiutil.ItemOf[*Org](r)
		if o.Plan != "enterprise" {
			chiutil.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]string{"org": o.ID, "contract": "MSA-" + o.ID + "-2026"})
	})

	// A WildcardFolder per org. Its instance list is shared; handlers see the org.
	regions := org.WildcardFolder("regions", "region", func(r chi.Router) {
		r.Get("/status", func(w http.ResponseWriter, r *http.Request) {
			o, _ := chiutil.ItemOf[*Org](r)
			writeJSON(w, map[string]string{"org": o.ID, "region": chi.URLParam(r, "region"), "status": "healthy"})
		})
		r.Get("/quota", func(w http.ResponseWriter, r *http.Request) {
			o, _ := chiutil.ItemOf[*Org](r)
			writeJSON(w, map[string]any{"org": o.ID, "region": chi.URLParam(r, "region"), "cpu": 64, "memoryGiB": 256})
		})
	}).Title("Regions").Description("Per-region status for this org")
	regions.Add("us-east-1", "N. Virginia")
	regions.Add("eu-west-1", "Ireland")

	// Level 2: scoped collections under the org.
	chiutil.ScopedObjectsFolder(org, "members", memberMapper{}).
		Title("Members").Description("Members of this org").FlatJSON()

	projects := chiutil.ScopedObjectsFolder(org, "projects", projectMapper{}).
		Title("Projects").Description("Projects of this org")
	project := projects.Scope()

	// Level 3: under a project. Deployments reuse the param name "id" that
	// projects use; each level still resolves its own item.
	chiutil.ScopedObjectsFolder(project, "deployments", deploymentMapper{}).
		Title("Deployments").Description("Deployments of this project")

	envs := project.WildcardFolder("environments", "env", func(r chi.Router) {
		r.Get("/config", func(w http.ResponseWriter, r *http.Request) {
			o, _ := chiutil.ItemOf[*Org](r)
			p, _ := chiutil.ItemOf[*Project](r)
			writeJSON(w, map[string]string{"org": o.ID, "project": p.ID, "env": chi.URLParam(r, "env"), "replicas": "3"})
		})
	}).Title("Environments")
	envs.Add("staging", "Pre-production")
	envs.Add("production", "Live traffic")

	// A top-level WildcardFolder, outside any scope, for comparison.
	services := bo.WildcardFolder("services", "name", func(r chi.Router) {
		r.Get("/info", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]string{"service": chi.URLParam(r, "name"), "version": "1.0.0"})
		})
	}).Title("Services")
	services.Add("gateway", "Front door")
	services.Add("worker", "Job runner")

	base := "http://127.0.0.1:" + port + "/backoffice"
	fmt.Printf("backoffice-scopes listening on :%s\n", port)
	fmt.Printf("  home:            %s/\n", base)
	fmt.Printf("  two levels deep: %s/orgs/acme/projects/api/deployments/d-101/\n", base)
	fmt.Printf("  wildcard in scope: %s/orgs/acme/projects/api/environments/staging/\n", base)
	fmt.Printf("  404 unknown item:  %s/orgs/acme/projects/nope/deployments/\n", base)
	fmt.Printf("  404 mistyped route: %s/orgs/globex/billing/refunds\n", base)
	fmt.Printf("  404 from a handler: %s/orgs/globex/billing/contract\n", base)
	fmt.Printf("  404 unknown wildcard instance: %s/orgs/acme/regions/ap-south-1/status\n", base)
	if err := http.ListenAndServe(":"+port, router); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// orgMapper is a plain ObjectMapper: the top of the hierarchy.
type orgMapper struct{}

func (orgMapper) ListItems() []chiutil.ObjectEntry {
	ids := make([]string, 0, len(orgs))
	for id := range orgs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]chiutil.ObjectEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, chiutil.ObjectEntry{ID: id, Description: orgs[id].Name})
	}
	return entries
}

func (orgMapper) GetItem(id string) (*Org, bool) {
	o, ok := orgs[id]
	return o, ok
}

func (orgMapper) Routes() []chiutil.ObjectRoute[*Org] {
	return []chiutil.ObjectRoute[*Org]{
		{Method: "GET", Path: "/details", Description: "Org details", Handler: func(o *Org, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"id": o.ID, "name": o.Name, "plan": o.Plan, "members": len(o.Members), "projects": len(o.Projects)})
		}},
		{Method: "POST", Path: "/suspend", Description: "Suspend this org", Action: chiutil.Form("Suspend org", []string{"reason"}),
			Handler: func(o *Org, w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]string{"org": o.ID, "suspended": "true", "reason": r.FormValue("reason")})
			}},
	}
}

// memberMapper lists the members of the enclosing org.
type memberMapper struct{}

func (memberMapper) ListItems(o *Org) []chiutil.ObjectEntry {
	var entries []chiutil.ObjectEntry
	for _, m := range o.Members {
		entries = append(entries, chiutil.ObjectEntry{ID: m.ID, Description: m.Role})
	}
	return entries
}

func (memberMapper) GetItem(o *Org, id string) (*Member, bool) {
	for _, m := range o.Members {
		if m.ID == id {
			return m, true
		}
	}
	return nil, false
}

func (memberMapper) Routes() []chiutil.ObjectRoute[*Member] { return nil }

// projectMapper lists the projects of the enclosing org.
type projectMapper struct{}

func (projectMapper) ListItems(o *Org) []chiutil.ObjectEntry {
	var entries []chiutil.ObjectEntry
	for _, p := range o.Projects {
		entries = append(entries, chiutil.ObjectEntry{ID: p.ID, Description: p.Repo})
	}
	return entries
}

func (projectMapper) GetItem(o *Org, id string) (*Project, bool) {
	for _, p := range o.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}

func (projectMapper) Routes() []chiutil.ObjectRoute[*Project] {
	return []chiutil.ObjectRoute[*Project]{
		{Method: "GET", Path: "/details", Description: "Project details", Handler: func(p *Project, w http.ResponseWriter, r *http.Request) {
			o, _ := chiutil.ItemOf[*Org](r)
			writeJSON(w, map[string]any{"org": o.ID, "id": p.ID, "repo": p.Repo, "deployments": len(p.Deployments)})
		}},
		{Method: "GET", Path: "/README.md", Description: "Project readme", Handler: func(p *Project, w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "# %s\n\nRepository `%s`, %d deployments.\n", p.ID, p.Repo, len(p.Deployments))
		}},
	}
}

// deploymentMapper lists the deployments of the enclosing project.
type deploymentMapper struct{}

func (deploymentMapper) ListItems(p *Project) []chiutil.ObjectEntry {
	var entries []chiutil.ObjectEntry
	for _, d := range p.Deployments {
		entries = append(entries, chiutil.ObjectEntry{ID: d.ID, Description: d.Version + " (" + d.Status + ")"})
	}
	return entries
}

func (deploymentMapper) GetItem(p *Project, id string) (*Deployment, bool) {
	for _, d := range p.Deployments {
		if d.ID == id {
			return d, true
		}
	}
	return nil, false
}

func (deploymentMapper) Routes() []chiutil.ObjectRoute[*Deployment] {
	return []chiutil.ObjectRoute[*Deployment]{
		{Method: "GET", Path: "/details", Description: "Deployment details", Handler: func(d *Deployment, w http.ResponseWriter, r *http.Request) {
			o, _ := chiutil.ItemOf[*Org](r)
			p, _ := chiutil.ItemOf[*Project](r)
			writeJSON(w, map[string]string{"org": o.ID, "project": p.ID, "id": d.ID, "version": d.Version, "status": d.Status})
		}},
	}
}
