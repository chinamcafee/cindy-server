package updates

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

func TestEndpointManifestServedAtClientPath(t *testing.T) {
	m := New(Deps{Config: &config.Config{
		Env:           "dev",
		Region:        "global",
		PublicBaseURL: "http://127.0.0.1:8080",
	}})
	server := httptest.NewServer(m.Routes())
	defer server.Close()

	for _, path := range []string{"/endpoint.json", "/api/endpoint.json"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest endpointManifest
		decodeErr := json.NewDecoder(resp.Body).Decode(&manifest)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d", path, resp.StatusCode)
		}
		if decodeErr != nil {
			t.Fatalf("%s decode: %v", path, decodeErr)
		}
		if manifest.SchemaVersion != 1 || manifest.Region != "global" {
			t.Fatalf("%s unexpected manifest: %+v", path, manifest)
		}
		if manifest.AuthAPIBaseURL != "http://127.0.0.1:8080" || manifest.ChatAPIBaseURL != "http://127.0.0.1:8080" {
			t.Fatalf("%s endpoints must point back at the server: %+v", path, manifest)
		}
	}
}
