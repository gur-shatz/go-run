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
		{"GET", "/details", (*TestAccount).Details, "Account details"},
		{"GET", "/settings", (*TestAccount).Settings, "Account settings"},
	}
}

var _ = Describe("ObjectMapper", func() {
	var (
		router *chi.Mux
		mapper *TestAccountMapper
	)

	BeforeEach(func() {
		router = chi.NewRouter()
		mapper = &TestAccountMapper{}

		// Add test accounts
		mapper.accounts.Store("acc-1", &TestAccount{ID: "acc-1", Name: "Acme Corp"})
		mapper.accounts.Store("acc-2", &TestAccount{ID: "acc-2", Name: "Globex Inc"})

		// Create folder and register mapper
		folder := chiutil.NewRouteFolder(router, "/backoffice")
		chiutil.ObjectsFolder(folder, "accounts", mapper).
			Title("Accounts").
			Description("Test accounts")
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

		It("should return 404 for non-existent item", func() {
			req := httptest.NewRequest("GET", "/backoffice/accounts/non-existent/details", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusNotFound))
		})
	})
})
