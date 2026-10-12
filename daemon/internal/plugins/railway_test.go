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

// Railway's whoami is Markdown; the account label is plain text, cut on a
// rune boundary.
func TestFirstLineLabel(t *testing.T) {
	if got := firstLine("**박성준** (@seongj-un)\nWorkspaces: …", 60); got != "박성준 (@seongj-un)" {
		t.Errorf("label = %q", got)
	}
	if got := firstLine("가나다라", 2); got != "가나" {
		t.Errorf("cut = %q", got)
	}
}

// A saved setup is reused only when it decodes cleanly and carries both the
// client id and the authorization endpoint; each defect alone rejects it.
func TestRailwaySavedSetupValidation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"complete", `{"client_id":"c","auth_url":"https://a","token_url":"https://t"}`, true},
		{"missing client id", `{"auth_url":"https://a"}`, false},
		{"missing auth url", `{"client_id":"c"}`, false},
		{"empty client id", `{"client_id":"","auth_url":"https://a"}`, false},
		{"empty auth url", `{"client_id":"c","auth_url":""}`, false},
		{"neither", `{}`, false},
		// A type error still fills the fields decoded before it.
		{"decode error with both fields", `{"client_id":"c","auth_url":"https://a","token_url":5}`, false},
		{"not json", `not json`, false},
	}
	for _, tc := range cases {
		if got := savedRailwaySetup(tc.in) != nil; got != tc.ok {
			t.Errorf("%s: accepted = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// A registration answer is usable only when it is valid JSON with a client id.
func TestRailwayRegisterClientRejectsBadAnswer(t *testing.T) {
	cases := []struct{ name, body string }{
		{"no client id", `{}`},
		{"empty client id", `{"client_id":""}`},
		{"not json", `nope`},
		// The first key is kept, the second is a type error.
		{"decode error after client id", `{"client_id":"x","client_id":5}`},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(tc.body))
		}))
		id, err := registerClient(context.Background(), srv.URL)
		srv.Close()
		if err == nil {
			t.Errorf("%s: accepted client id %q", tc.name, id)
		}
	}
}

// firstLine edge cases: exactly max runes stays whole, one over is cut, and
// surrounding blank lines do not hide the first real line.
func TestFirstLineBounds(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"가나", 2, "가나"},
		{"가나다", 2, "가나"},
		{"\n\nabc\ndef", 10, "abc"},
		{"  **x**  ", 10, "x"},
	}
	for _, tc := range cases {
		if got := firstLine(tc.in, tc.max); got != tc.want {
			t.Errorf("firstLine(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
