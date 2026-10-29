package gcponboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Dormeto/cli/internal/gcp"
)

type tokens struct{}

func (tokens) AccessToken(context.Context) (string, error)           { return "tok", nil }
func (tokens) IdentityToken(context.Context, string) (string, error) { return "id", nil }
func (tokens) Account(context.Context) (string, error)               { return "a@b.c", nil }

var cond = &Condition{Title: "t", Expression: "resource.name.startsWith('x')"}

func TestAddToEmptyPolicy(t *testing.T) {
	p := &Policy{Etag: "e"}
	added := Add(p, Grant{Role: "roles/a", Member: "serviceAccount:s@p.iam.gserviceaccount.com"})
	want := []Binding{{Role: "roles/a", Members: []string{"serviceAccount:s@p.iam.gserviceaccount.com"}}}
	if !reflect.DeepEqual(p.Bindings, want) || len(added) != 1 {
		t.Fatalf("got %+v %+v", p.Bindings, added)
	}
}

func TestAddExistingMemberNoChange(t *testing.T) {
	p := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:x", "user:y"}}}}
	if added := Add(p, Grant{Role: "roles/a", Member: "user:y"}); len(added) != 0 {
		t.Fatalf("added %+v", added)
	}
	if len(p.Bindings) != 1 || len(p.Bindings[0].Members) != 2 {
		t.Fatalf("changed %+v", p.Bindings)
	}
}

func TestAddJoinsBindingOfSameRoleAndCondition(t *testing.T) {
	p := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:x"}, Condition: &Condition{Title: "t", Expression: cond.Expression, Description: "other text"}}}}
	Add(p, Grant{Role: "roles/a", Member: "user:y", Condition: cond})
	if len(p.Bindings) != 1 || !reflect.DeepEqual(p.Bindings[0].Members, []string{"user:x", "user:y"}) {
		t.Fatalf("got %+v", p.Bindings)
	}
}

func TestAddConditionalBesideUnconditional(t *testing.T) {
	p := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:x"}}}}
	Add(p, Grant{Role: "roles/a", Member: "user:y", Condition: cond})
	if len(p.Bindings) != 2 || p.Bindings[0].Condition != nil || p.Bindings[1].Condition == nil ||
		!reflect.DeepEqual(p.Bindings[0].Members, []string{"user:x"}) {
		t.Fatalf("got %+v", p.Bindings)
	}
}

func TestRemoveKeepsOtherMembers(t *testing.T) {
	p := &Policy{Bindings: []Binding{
		{Role: "roles/a", Members: []string{"user:x", "user:y"}},
		{Role: "roles/b", Members: []string{"user:y"}},
		{Role: "roles/c", Members: []string{"user:z"}},
	}}
	removed := Remove(p, Grant{Role: "roles/a", Member: "user:y"}, Grant{Role: "roles/b", Member: "user:y"})
	want := []Binding{{Role: "roles/a", Members: []string{"user:x"}}, {Role: "roles/c", Members: []string{"user:z"}}}
	if !reflect.DeepEqual(p.Bindings, want) || len(removed) != 2 {
		t.Fatalf("got %+v %+v", p.Bindings, removed)
	}
}

func TestRemoveLeavesBindingOfOtherCondition(t *testing.T) {
	p := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:y"}}, {Role: "roles/a", Members: []string{"user:y"}, Condition: cond}}}
	Remove(p, Grant{Role: "roles/a", Member: "user:y", Condition: cond})
	if len(p.Bindings) != 1 || p.Bindings[0].Condition != nil {
		t.Fatalf("got %+v", p.Bindings)
	}
}

func TestRemoveDeletedServiceAccountByEmail(t *testing.T) {
	p := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"deleted:serviceAccount:s@p.iam.gserviceaccount.com?uid=123", "deleted:serviceAccount:other@p.iam.gserviceaccount.com?uid=9", "user:x"}}}}
	removed := Remove(p, Grant{Role: "roles/a", Member: "serviceAccount:s@p.iam.gserviceaccount.com"})
	if !reflect.DeepEqual(p.Bindings[0].Members, []string{"deleted:serviceAccount:other@p.iam.gserviceaccount.com?uid=9", "user:x"}) {
		t.Fatalf("got %+v", p.Bindings)
	}
	if len(removed) != 1 || removed[0].Member != "deleted:serviceAccount:s@p.iam.gserviceaccount.com?uid=123" {
		t.Fatalf("removed %+v", removed)
	}
}

func TestDiff(t *testing.T) {
	before := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:x", "user:y"}}, {Role: "roles/b", Members: []string{"user:z"}, Condition: cond}}}
	after := &Policy{Bindings: []Binding{{Role: "roles/a", Members: []string{"user:x"}}, {Role: "roles/b", Members: []string{"user:z"}, Condition: cond}, {Role: "roles/c", Members: []string{"user:w"}}}}
	added, removed := Diff(before, after)
	if !reflect.DeepEqual(added, []Grant{{Role: "roles/c", Member: "user:w"}}) ||
		!reflect.DeepEqual(removed, []Grant{{Role: "roles/a", Member: "user:y"}}) {
		t.Fatalf("added %+v removed %+v", added, removed)
	}
	if a, r := Diff(before, before); len(a)+len(r) != 0 {
		t.Fatalf("self diff %+v %+v", a, r)
	}
	if a, _ := Diff(nil, after); len(a) != 3 {
		t.Fatalf("nil before %+v", a)
	}
}

func TestPolicyJSONKeepsUnknownFields(t *testing.T) {
	in := `{"version":1,"etag":"e","bindings":[],"auditConfigs":[{"service":"allServices"}]}`
	var p Policy
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(&p)
	if !strings.Contains(string(b), `"auditConfigs":[{"service":"allServices"}]`) {
		t.Fatalf("lost: %s", b)
	}
}

type call struct{ method, path, query, body string }

// fake serves h for every call; the client sends to it.
func fake(t *testing.T, h func(n int, c call) (int, string)) (*gcp.Client, string, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		c := call{r.Method, r.URL.Path, r.URL.RawQuery, string(b)}
		calls = append(calls, c)
		n := len(calls)
		mu.Unlock()
		code, body := h(n, c)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := pause
	pause = func(context.Context, int) error { return nil }
	t.Cleanup(func() { pause = old })
	return &gcp.Client{HTTP: srv.Client(), Tokens: tokens{}}, srv.URL, &calls
}

func policyOf(t *testing.T, body string) Policy {
	t.Helper()
	var in struct{ Policy Policy }
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	return in.Policy
}

func addX(p *Policy) error { Add(p, Grant{Role: "roles/a", Member: "user:x"}); return nil }

func TestUpdateRetriesAfterEtagConflict(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) {
		switch n {
		case 1:
			return 200, `{"version":1,"etag":"e1"}`
		case 2:
			return 409, `{"error":{"code":409,"message":"etag","status":"ABORTED"}}`
		case 3:
			return 200, `{"version":1,"etag":"e2","bindings":[{"role":"roles/b","members":["user:z"]}]}`
		default:
			return 200, `{"version":1,"etag":"e3","bindings":[{"role":"roles/b","members":["user:z"]},{"role":"roles/a","members":["user:x"]}]}`
		}
	})
	res, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls %+v", *calls)
	}
	if got := policyOf(t, (*calls)[3].body); got.Etag != "e2" || len(got.Bindings) != 2 {
		t.Fatalf("second write %+v", got)
	}
	if res.Before.Etag != "e2" || res.After.Etag != "e3" {
		t.Fatalf("result %+v", res)
	}
	if a, r := Diff(res.Before, res.After); len(a) != 1 || len(r) != 0 {
		t.Fatalf("diff %+v %+v", a, r)
	}
}

func TestUpdateGivesUpAfterFiveConflicts(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) {
		if strings.HasSuffix(c.path, ":getIamPolicy") {
			return 200, `{"etag":"e"}`
		}
		return 409, `{"error":{"code":409,"message":"etag","status":"ABORTED"}}`
	})
	_, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX)
	if !gcp.IsConflict(err) || len(*calls) != 10 {
		t.Fatalf("err %v calls %d", err, len(*calls))
	}
}

func TestUpdateDoesNotRetryOtherErrors(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) {
		if strings.HasSuffix(c.path, ":getIamPolicy") {
			return 200, `{"etag":"e"}`
		}
		return 403, `{"error":{"code":403,"message":"no","status":"PERMISSION_DENIED"}}`
	})
	_, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX)
	if !gcp.IsDenied(err) || len(*calls) != 2 {
		t.Fatalf("err %v calls %d", err, len(*calls))
	}
}

func TestUpdateWithoutChangeWritesNothing(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) {
		return 200, `{"version":1,"etag":"e","bindings":[{"role":"roles/a","members":["user:x"]}]}`
	})
	res, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX)
	if err != nil || len(*calls) != 1 || res.After.Etag != "e" {
		t.Fatalf("err %v calls %d", err, len(*calls))
	}
}

func TestUpdateMutateErrorStops(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) { return 200, `{"etag":"e"}` })
	boom := errors.New("boom")
	_, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, func(*Policy) error { return boom })
	if !errors.Is(err, boom) || len(*calls) != 1 {
		t.Fatalf("err %v calls %d", err, len(*calls))
	}
}

func TestUpdateWritesVersion3WithCondition(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) { return 200, `{"version":1,"etag":"e"}` })
	_, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, func(p *Policy) error {
		Add(p, Grant{Role: "roles/a", Member: "user:x", Condition: cond})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*calls)[0].body, `"requestedPolicyVersion":3`) {
		t.Fatalf("get body %s", (*calls)[0].body)
	}
	set := (*calls)[1].body
	if got := policyOf(t, set); got.Version != 3 || got.Bindings[0].Condition == nil {
		t.Fatalf("set body %s", set)
	}
	if strings.Contains(set, `"description"`) {
		t.Fatalf("empty description sent: %s", set)
	}
}

func TestUpdateKeepsVersionWithoutCondition(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) { return 200, `{"version":1,"etag":"e"}` })
	if _, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX); err != nil {
		t.Fatal(err)
	}
	if got := policyOf(t, (*calls)[1].body); got.Version != 1 {
		t.Fatalf("version %d", got.Version)
	}
}

func TestResourcePolicies(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rp               ResourcePolicy
		get, set         string
		query, postQuery bool
	}{
		{"project", ProjectPolicy("p"), "https://cloudresourcemanager.googleapis.com/v3/projects/p:getIamPolicy", "https://cloudresourcemanager.googleapis.com/v3/projects/p:setIamPolicy", false, false},
		{"serviceAccount", ServiceAccountPolicy("p", "s@p.iam.gserviceaccount.com"), "https://iam.googleapis.com/v1/projects/p/serviceAccounts/s@p.iam.gserviceaccount.com:getIamPolicy?options.requestedPolicyVersion=3", "https://iam.googleapis.com/v1/projects/p/serviceAccounts/s@p.iam.gserviceaccount.com:setIamPolicy", false, true},
		{"queue", QueuePolicy("p", "r", "q"), "https://cloudtasks.googleapis.com/v2/projects/p/locations/r/queues/q:getIamPolicy", "https://cloudtasks.googleapis.com/v2/projects/p/locations/r/queues/q:setIamPolicy", false, false},
		{"run", RunPolicy("p", "r", "svc"), "https://run.googleapis.com/v2/projects/p/locations/r/services/svc:getIamPolicy?options.requestedPolicyVersion=3", "https://run.googleapis.com/v2/projects/p/locations/r/services/svc:setIamPolicy", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.rp.Get != tc.get || tc.rp.Set != tc.set || tc.rp.GetByQuery != tc.query || tc.rp.PostByQuery != tc.postQuery {
				t.Fatalf("got %+v", tc.rp)
			}
		})
	}
}

func TestUpdateGetsByQueryForCloudRun(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) { return 200, `{"etag":"e"}` })
	rp := ResourcePolicy{Get: url + "/r:getIamPolicy?options.requestedPolicyVersion=3", Set: url + "/r:setIamPolicy", GetByQuery: true}
	if _, err := Update(context.Background(), client, rp, addX); err != nil {
		t.Fatal(err)
	}
	g := (*calls)[0]
	if g.method != "GET" || g.query != "options.requestedPolicyVersion=3" || g.body != "" {
		t.Fatalf("get %+v", g)
	}
}

func TestUpdatePostsAnEmptyBodyForServiceAccounts(t *testing.T) {
	client, url, calls := fake(t, func(n int, c call) (int, string) { return 200, `{"etag":"e"}` })
	rp := ResourcePolicy{Get: url + "/r:getIamPolicy?options.requestedPolicyVersion=3", Set: url + "/r:setIamPolicy", PostByQuery: true}
	if _, err := Update(context.Background(), client, rp, addX); err != nil {
		t.Fatal(err)
	}
	g := (*calls)[0]
	if g.method != "POST" || g.query != "options.requestedPolicyVersion=3" || g.body != "" {
		t.Fatalf("get %+v", g)
	}
}

func TestUpdateKeepsOthersBindingFieldsUntouched(t *testing.T) {
	theirs := `{"condition":{"description":"d","expression":"e","location":"l","title":"t","vendor":1},"members":["user:o"],"role":"roles/o","unknown":{"k":[1,2]}}`
	var set string
	client, url, _ := fake(t, func(n int, c call) (int, string) {
		if strings.HasSuffix(c.path, ":setIamPolicy") {
			set = c.body
			return 200, `{"etag":"e2"}`
		}
		return 200, `{"version":3,"etag":"e","bindings":[` + theirs + `]}`
	})
	_, err := Update(context.Background(), client, ResourcePolicy{Get: url + "/r:getIamPolicy", Set: url + "/r:setIamPolicy"}, addX)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Policy struct{ Bindings []json.RawMessage }
	}
	if err := json.Unmarshal([]byte(set), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Policy.Bindings) != 2 || string(in.Policy.Bindings[0]) != theirs {
		t.Fatalf("binding changed:\n got %s\nwant %s", in.Policy.Bindings, theirs)
	}
}
