package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteTimeout = 60 * time.Second

// Remote stores objects through the authenticated edge object gateway.
type Remote struct {
	base   string
	token  string
	client *http.Client
}

// NewRemote requires both the gateway URL and its credential.
func NewRemote(rawURL, token string) (*Remote, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("OBJECT_STORAGE_URL must be an absolute HTTP(S) URL")
	}
	if token == "" {
		return nil, errors.New("OBJECT_STORAGE_TOKEN is required with OBJECT_STORAGE_URL")
	}
	return &Remote{base: strings.TrimRight(rawURL, "/"), token: token, client: &http.Client{Timeout: remoteTimeout}}, nil
}

// Put writes content under key.
func (s *Remote) Put(key string, content []byte) error {
	response, err := s.do(http.MethodPut, key, content)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return expectStatus(response, key, http.StatusNoContent)
}

// Get reads a stored object.
func (s *Remote) Get(key string) ([]byte, error) {
	response, err := s.do(http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("storage: get %s: %w", key, fs.ErrNotExist)
	}
	if err := expectStatus(response, key, http.StatusOK); err != nil {
		return nil, err
	}
	return io.ReadAll(response.Body)
}

// Delete removes a stored object. The gateway succeeds for absent keys.
func (s *Remote) Delete(key string) error {
	response, err := s.do(http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return expectStatus(response, key, http.StatusNoContent)
}

func (s *Remote) do(method, key string, content []byte) (*http.Response, error) {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, ErrEscape
		}
		segments[index] = url.PathEscape(segment)
	}
	request, err := http.NewRequest(method, s.base+"/"+strings.Join(segments, "/"), bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	return s.client.Do(request)
}

func expectStatus(response *http.Response, key string, want int) error {
	if response.StatusCode != want {
		return fmt.Errorf("storage: %s %s: unexpected status %d", response.Request.Method, key, response.StatusCode)
	}
	return nil
}
