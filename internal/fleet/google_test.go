package fleet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

var connection = GoogleConnection{
	Project: "dormeto-dev", ProjectNumber: "1034397096264", Pool: "dmt-dev", Provider: "aws", Scopes: []string{"projects/dormeto-dev"},
	ServiceAccounts: GoogleServiceAccounts{Discovery: "dmt-dev-discovery@dormeto-dev.iam.gserviceaccount.com", Command: "dmt-dev-command@dormeto-dev.iam.gserviceaccount.com"},
	Routers:         []GoogleRouter{{Project: "dormeto-dev", Region: "europe-west1", Controller: "https://dmtr-europe-west1-controller-abc-ew.a.run.app", Version: "0.40.0"}},
	GoogleAdded:     []string{}, ConnectedAt: "2026-11-09T10:00:00Z", ConnectedBy: "me@example.com", CLI: "0.14.0",
}

func TestGoogleConnectionReadsTheRecordOrNone(t *testing.T) {
	answer := `{"connection":null}`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/iam/discovery/google" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		io.WriteString(w, answer)
	})
	got, err := c.GoogleConnection(context.Background())
	if err != nil || got != nil {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := json.Marshal(map[string]any{"connection": connection})
	answer = string(b)
	got, err = c.GoogleConnection(context.Background())
	if err != nil || got == nil || !reflect.DeepEqual(*got, connection) {
		t.Fatalf("%+v %v", got, err)
	}
}

// The record goes as the manager's route reads it (packages/api googleRecord): its field names, a router's controller and
// version left out when unknown.
func TestPutGoogleConnectionSendsTheRecordAndReadsTheStoredOne(t *testing.T) {
	sent := map[string]any{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/iam/discovery/google" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sent)
		io.WriteString(w, `{"connection":`+string(b)+`}`)
	})
	rec := connection
	rec.Routers = append(rec.Routers, GoogleRouter{Project: "dormeto-dev", Region: "europe-west2"})
	got, err := c.PutGoogleConnection(context.Background(), rec)
	if err != nil || got == nil || !reflect.DeepEqual(*got, rec) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, k := range []string{"project", "projectNumber", "pool", "provider", "scopes", "serviceAccounts", "routers", "googleAdded", "connectedAt", "connectedBy", "cli"} {
		if _, ok := sent[k]; !ok {
			t.Errorf("no %s in %v", k, sent)
		}
	}
	routers := sent["routers"].([]any)
	if second := routers[1].(map[string]any); len(second) != 2 || second["region"] != "europe-west2" {
		t.Fatalf("a router without its controller: %v", second)
	}
}

func TestDeleteGoogleConnection(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/iam/discovery/google" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		io.WriteString(w, `{"connection":null}`)
	})
	if err := c.DeleteGoogleConnection(context.Background()); err != nil || calls != 1 {
		t.Fatalf("%v %d", err, calls)
	}
}
