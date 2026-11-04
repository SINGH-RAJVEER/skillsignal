package storage

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteRoundTrip(t *testing.T) {
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	store, err := NewRemote(server.URL+"/__storage/", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("resumes/a.pdf", []byte("resume")); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects["/__storage/resumes/a.pdf"]; !ok {
		t.Fatalf("object stored under unexpected paths: %v", objects)
	}
	body, err := store.Get("resumes/a.pdf")
	if err != nil || string(body) != "resume" {
		t.Fatalf("get = %q, %v", body, err)
	}
	if err := store.Delete("resumes/a.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("resumes/a.pdf"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("get after delete = %v, want fs.ErrNotExist", err)
	}
}

func TestRemoteRejectsUnsafeKeysAndFailedWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	store, err := NewRemote(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("../outside"); !errors.Is(err, ErrEscape) {
		t.Fatalf("traversal = %v, want ErrEscape", err)
	}
	if err := store.Put("resumes/a.pdf", []byte("resume")); err == nil {
		t.Fatal("rejected write reported success")
	}
}

func TestNewRemoteRequiresCredential(t *testing.T) {
	if _, err := NewRemote("https://example.com/__storage", ""); err == nil {
		t.Fatal("missing token accepted")
	}
}
