package chiutil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/gur-shatz/go-run/pkg/chiutil"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Test item struct
type TestAccount struct {
	ID   string
	Name string
}

func (a *TestAccount) Details(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"id":   a.ID,
		"name": a.Name,
	})
}

func (a *TestAccount) Settings(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"theme": "dark",
	})
}

func (a *TestAccount) Report(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("reported " + a.ID))
}

func (a *TestAccount) ReportForm(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("explicit form " + a.ID))
}

func (a *TestAccount) Readme(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("# Account\n\n" + a.ID + "\n"))
}

// Test mapper implementation
type TestAccountMapper struct {
	accounts sync.Map
}

func (m *TestAccountMapper) ListItems() []chiutil.ObjectEntry {
	var entries []chiutil.ObjectEntry
	m.accounts.Range(func(key, value any) bool {
		acc := value.(*TestAccount)
		entries = append(entries, chiutil.ObjectEntry{
			ID:          acc.ID,
			Description: acc.Name,
		})
		return true
	})
	return entries
}

func (m *TestAccountMapper) GetItem(id string) (*TestAccount, bool) {
	if val, ok := m.accounts.Load(id); ok {
		return val.(*TestAccount), true
	}
	return nil, false
}

func (m *TestAccountMapper) Routes() []chiutil.ObjectRoute[*TestAccount] {
	return []chiutil.ObjectRoute[*TestAccount]{
		{Method: "GET", Path: "/details", Handler: (*TestAccount).Details, Description: "Account details"},
		{Method: "GET", Path: "/settings", Handler: (*TestAccount).Settings, Description: "Account settings"},
	}
}

type actionTestAccountMapper struct {
	*TestAccountMapper
}

func (m actionTestAccountMapper) Routes() []chiutil.ObjectRoute[*TestAccount] {
	return []chiutil.ObjectRoute[*TestAccount]{
		{
			Method:      http.MethodPost,
			Path:        "/report",
			Handler:     (*TestAccount).Report,
			Description: "Report account",
			Action:      chiutil.Form("Report account", []string{"state"}),
		},
	}
}

type explicitGetPostAccountMapper struct {
	*TestAccountMapper
}

func (m explicitGetPostAccountMapper) Routes() []chiutil.ObjectRoute[*TestAccount] {
	return []chiutil.ObjectRoute[*TestAccount]{
		{Method: http.MethodGet, Path: "/report", Handler: (*TestAccount).ReportForm, Description: "Report account form"},
		{
			Method:      http.MethodPost,
			Path:        "/report",
			Handler:     (*TestAccount).Report,
			Description: "Report account",
			Action:      chiutil.Form("Report account", []string{"state"}),
		},
	}
}

type markdownAccountMapper struct {
	*TestAccountMapper
}

func (m markdownAccountMapper) Routes() []chiutil.ObjectRoute[*TestAccount] {
	return []chiutil.ObjectRoute[*TestAccount]{
		{Method: http.MethodGet, Path: "/readme.md", Handler: (*TestAccount).Readme, Description: "Account README"},
	}
}

type hiddenAccountMapper struct {
	*TestAccountMapper
}

func (m hiddenAccountMapper) Routes() []chiutil.ObjectRoute[*TestAccount] {
	return []chiutil.ObjectRoute[*TestAccount]{
		{Method: http.MethodGet, Path: "/details", Handler: (*TestAccount).Details, Description: "Account details"},
		{Method: http.MethodGet, Path: "/settings", Handler: (*TestAccount).Settings, Description: "Account settings", Hidden: true},
	}
}

var _ = Describe("ObjectMapper", func() {
	var (
		router *chi.Mux
		mapper *TestAccountMapper
	)

	setupFolder := func(flatJSON bool) {
		router = chi.NewRouter()
		mapper = &TestAccountMapper{}

		// Add test accounts
		mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
		mapper.accounts.Store("acc-2", &TestAccount{ID: "acc-2", Name: "Globex Inc"})

		// Create folder and register mapper
		folder := chiutil.NewRouteFolder(router, "/backoffice")
		objectsFolder := chiutil.ObjectsFolder(folder, "accounts", mapper).
			Title("Accounts").
			Description("Test accounts")
		if flatJSON {
			objectsFolder.FlatJSON()
		}
	}

	BeforeEach(func() {
		setupFolder(false)
	})

	Describe("Listing", func() {
		It("should list all items", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/index.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))

			var index chiutil.FolderIndex
			err := json.Unmarshal(w.Body.Bytes(), &index)
			Expect(err).NotTo(HaveOccurred())
			Expect(index.Entries).To(HaveLen(2))
			Expect(index.Title).To(Equal("Accounts"))
		})

		It("serves a collection index handler as the default preview", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", mapper).Index(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("accounts overview"))
			})

			req := httptest.NewRequest("GET", "/backoffice/accounts/index.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			var index chiutil.FolderIndex
			Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
			Expect(index.HasIndex).To(BeTrue())
			Expect(index.Entries).To(HaveLen(1))
			Expect(index.Entries[0].Name).To(Equal("_index"))

			req = httptest.NewRequest("GET", "/backoffice/accounts/?preview=true", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("accounts overview"))

			req = httptest.NewRequest("GET", "/backoffice/accounts/_index", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("accounts overview"))
		})

		It("serves a default collection preview table from listed objects", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/?preview=true", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			body := w.Body.String()
			Expect(body).To(ContainSubstring("Accounts"))
			Expect(body).To(ContainSubstring(`href="acc-1/"`))
			Expect(body).To(ContainSubstring("Acme Corp"))
			Expect(body).To(ContainSubstring(`href="acc-2/"`))
			Expect(body).To(ContainSubstring("Globex Inc"))
		})
	})

	Describe("Item routes", func() {
		It("should list routes for an item", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1/index.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))

			var index chiutil.FolderIndex
			err := json.Unmarshal(w.Body.Bytes(), &index)
			Expect(err).NotTo(HaveOccurred())
			Expect(index.Entries).To(HaveLen(2))

			// Find details route
			var detailsEntry *chiutil.RouteEntry
			for _, e := range index.Entries {
				if e.Name == "details" {
					detailsEntry = e
					break
				}
			}
			Expect(detailsEntry).NotTo(BeNil())
			Expect(detailsEntry.Description).To(Equal("Account details"))
		})

		It("serves a default item preview table with detail-select links", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1/?preview=true", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			body := w.Body.String()
			Expect(body).To(ContainSubstring("Acc-1"))
			Expect(body).To(ContainSubstring("chiutil:select"))
			Expect(body).To(ContainSubstring("details"))
			Expect(body).To(ContainSubstring("Account details"))
			Expect(body).NotTo(ContainSubstring(`href="details"`))
		})

		It("serves a per-object item index handler as the default preview", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", mapper).ItemIndex(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("object page for " + chi.URLParam(r, "id")))
			})

			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1/?preview=true", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("object page for acc-1"))

			req = httptest.NewRequest("GET", "/backoffice/accounts/acc-1/index.json", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			var index chiutil.FolderIndex
			Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
			var mainEntry *chiutil.RouteEntry
			for _, entry := range index.Entries {
				if entry.Name == "_index" {
					mainEntry = entry
					break
				}
			}
			Expect(mainEntry).NotTo(BeNil())
			Expect(mainEntry.Method).To(Equal(http.MethodGet))
			Expect(mainEntry.Path).To(Equal("_index"))

			req = httptest.NewRequest("GET", "/backoffice/accounts/acc-1/_index", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("object page for acc-1"))
		})

		It("should call the item's handler", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1/details", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))

			var result map[string]string
			err := json.Unmarshal(w.Body.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result["id"]).To(Equal("acc-1"))
			Expect(result["name"]).To(Equal("Acme Corp"))
		})

		It("keeps hidden routes callable but omits them from the item index", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", hiddenAccountMapper{TestAccountMapper: mapper})

			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1/index.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			var index chiutil.FolderIndex
			Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
			Expect(index.Entries).To(HaveLen(1))
			Expect(index.Entries[0].Name).To(Equal("details"))

			req = httptest.NewRequest("GET", "/backoffice/accounts/acc-1/settings", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			var result map[string]string
			Expect(json.Unmarshal(w.Body.Bytes(), &result)).To(Succeed())
			Expect(result["theme"]).To(Equal("dark"))
		})

		It("marks .md object routes for markdown preview rendering", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", markdownAccountMapper{TestAccountMapper: mapper})

			req := httptest.NewRequest(http.MethodGet, "/backoffice/accounts/acc-1/readme.md?preview=true", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(Equal("text/markdown; charset=utf-8"))
			Expect(w.Header().Get("X-Chiutil-Viewer")).To(Equal("markdown"))
		})

		It("serves GET action forms for POST object routes", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", actionTestAccountMapper{TestAccountMapper: mapper})

			req := httptest.NewRequest(http.MethodGet, "/backoffice/accounts/acc-1/report", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
			Expect(w.Body.String()).To(ContainSubstring("Report account"))

			req = httptest.NewRequest(http.MethodPost, "/backoffice/accounts/acc-1/report", nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("reported acc-1"))
		})

		It("does not auto-register a GET action when an explicit GET route exists", func() {
			router = chi.NewRouter()
			mapper = &TestAccountMapper{}
			mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
			folder := chiutil.NewRouteFolder(router, "/backoffice")
			chiutil.ObjectsFolder(folder, "accounts", explicitGetPostAccountMapper{TestAccountMapper: mapper})

			req := httptest.NewRequest(http.MethodGet, "/backoffice/accounts/acc-1/report", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("explicit form acc-1"))
		})

		It("should return 404 for non-existent item", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/non-existent/details", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusNotFound))
		})
	})

	Describe("Flat JSON", func() {
		BeforeEach(func() {
			setupFolder(true)
		})

		It("should list items as flat JSON documents", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/index.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))

			var index chiutil.FolderIndex
			err := json.Unmarshal(w.Body.Bytes(), &index)
			Expect(err).NotTo(HaveOccurred())
			Expect(index.Entries).To(HaveLen(2))

			var acc1Entry *chiutil.RouteEntry
			for _, e := range index.Entries {
				if e.Name == "acc-1" {
					acc1Entry = e
					break
				}
			}
			Expect(acc1Entry).NotTo(BeNil())
			Expect(acc1Entry.Path).To(Equal("acc-1.json"))
			Expect(acc1Entry.IsFolder).To(BeFalse())
		})

		It("should serve an item as JSON", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/acc-1.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(Equal("application/json"))

			var result TestAccount
			err := json.Unmarshal(w.Body.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ID).To(Equal("acc-1"))
			Expect(result.Name).To(Equal("Acme Corp"))
		})

		It("should return 404 for non-existent flat JSON item", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/non-existent.json", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusNotFound))
		})
	})
})
