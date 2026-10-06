package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Railway refuses the device flow for dynamically registered clients
// ("device_code is not allowed for this client", M0 2026-10-06), so the
// client is registered for the authorization code grant with a loopback
// redirect. Railway accepts any port on a registered loopback URI.
func TestRailwayRegistersLoopbackClient(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"client_id": "rlwy_oaci_test"})
	}))
	defer srv.Close()

	id, err := registerClient(context.Background(), srv.URL)
	if err != nil || id != "rlwy_oaci_test" {
		t.Fatalf("registerClient = %q, %v", id, err)
	}
	want := map[string]any{
		"grant_types":                []any{"authorization_code", "refresh_token"},
		"response_types":             []any{"code"},
		"redirect_uris":              []any{"http://127.0.0.1/callback"},
		"application_type":           "native",
		"token_endpoint_auth_method": "none",
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

// A setup saved by the device-flow build has no authorization endpoint and
// a client that cannot use the code grant, so it must be registered again.
func TestRailwaySavedSetupNeedsAuthURL(t *testing.T) {
	old := `{"resource":"https://mcp.railway.com","client_id":"rlwy_oaci_old","token_url":"https://backboard.railway.com/oauth/token","device_auth_url":"https://backboard.railway.com/oauth/device/auth"}`
	if s := savedRailwaySetup(old); s != nil {
		t.Fatalf("device-flow setup reused: %+v", s)
	}
	cur := `{"resource":"https://mcp.railway.com","client_id":"rlwy_oaci_new","auth_url":"https://backboard.railway.com/oauth/auth","token_url":"https://backboard.railway.com/oauth/token"}`
	s := savedRailwaySetup(cur)
	if s == nil || s.ClientID != "rlwy_oaci_new" || s.AuthURL == "" {
		t.Fatalf("current setup = %+v", s)
	}
	if savedRailwaySetup("not json") != nil {
		t.Fatal("garbage accepted")
	}
}
