package htmlview

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testAccount struct {
	ID     string
	Name   string
	Status string
	Secret string
}

type taggedAccount struct {
	ID     string `htmlview:"Account ID"`
	Secret string `htmlview:"-"`
}

func TestRenderStructWithColumns(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	w := httptest.NewRecorder()

	Render(testAccount{
		ID:     "acct-1",
		Name:   "<Acme>",
		Status: "active",
		Secret: "hidden",
	}).
		WithTitle("Account").
		WithColumns("ID", "Name", "Status").
		ServeHTTP(w, req)

	body := w.Body.String()
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	for _, want := range []string{"Account", "acct-1", "&lt;Acme&gt;", "active"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<td>hidden</td>") {
		t.Fatalf("body contains unselected field: %s", body)
	}
}

func TestRenderSliceWithColumns(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	w := httptest.NewRecorder()

	Render([]testAccount{
		{ID: "acct-1", Name: "Acme", Status: "active"},
		{ID: "acct-2", Name: "Globex", Status: "paused"},
	}).
		WithColumns("ID", "Status").
		ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"acct-1", "active", "acct-2", "paused"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Acme") || strings.Contains(body, "Globex") {
		t.Fatalf("body contains unselected columns: %s", body)
	}
}

func TestRenderSliceWithNilPointerFirst(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	w := httptest.NewRecorder()

	Render([]*testAccount{
		nil,
		{ID: "acct-2", Status: "paused"},
	}).
		WithColumns("ID", "Status").
		ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"ID", "Status", "acct-2", "paused"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q:\n%s", want, body)
		}
	}
}

func TestRenderUsesHTMLViewTags(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	w := httptest.NewRecorder()

	Render(taggedAccount{ID: "acct-1", Secret: "hidden"}).ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Account ID") {
		t.Fatalf("body does not contain tagged label:\n%s", body)
	}
	if strings.Contains(body, "<td>hidden</td>") || strings.Contains(body, "Secret") {
		t.Fatalf("body contains hidden tagged field:\n%s", body)
	}
}

func TestLoader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	w := httptest.NewRecorder()

	Loader(func(r *http.Request) ([]testAccount, error) {
		if r.URL.Path != "/accounts" {
			t.Fatalf("loader received path %q", r.URL.Path)
		}
		return []testAccount{{ID: "acct-1"}}, nil
	}).WithColumns("ID").ServeHTTP(w, req)

	if body := w.Body.String(); !strings.Contains(body, "acct-1") {
		t.Fatalf("body does not contain loaded value:\n%s", body)
	}
}

func TestLoaderError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	w := httptest.NewRecorder()

	Loader(func(r *http.Request) ([]testAccount, error) {
		return nil, errors.New("load failed")
	}).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	if body := w.Body.String(); !strings.Contains(body, "load failed") {
		t.Fatalf("body does not contain loader error: %s", body)
	}
}
