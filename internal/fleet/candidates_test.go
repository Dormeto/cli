package fleet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const (
	arnA = "arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/api/aaaaaaaaaaaaaaaa"
	arnB = "arn:aws:elasticloadbalancing:eu-west-1:222222222222:targetgroup/api/bbbbbbbbbbbbbbbb"
	arnC = "arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/web/cccccccccccccccc"
)

func candidates() []Candidate {
	return []Candidate{
		{ID: arnA, Account: "111111111111", TargetGroupARN: arnA, Status: "pending-onboarding"},
		{ID: arnB, Account: "222222222222", TargetGroupARN: arnB, Status: "pending-onboarding"},
		{ID: arnC, Account: "111111111111", TargetGroupARN: arnC, Status: "pending-onboarding"},
	}
}

func TestTargetGroupName(t *testing.T) {
	for in, want := range map[string]string{arnA: "api", "plain": "plain", "": ""} {
		if got := TargetGroupName(in); got != want {
			t.Errorf("TargetGroupName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveCandidatesByIDAndUniqueName(t *testing.T) {
	got, err := ResolveCandidates(candidates(), []string{arnA, "web", "web"})
	if err != nil || len(got) != 2 || got[0].ID != arnA || got[1].ID != arnC {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResolveCandidatesAmbiguousNameListsBothARNs(t *testing.T) {
	_, err := ResolveCandidates(candidates(), []string{"web", "api"})
	if err == nil || !strings.Contains(err.Error(), "api names several target groups:") || !strings.Contains(err.Error(), arnA) || !strings.Contains(err.Error(), arnB) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCandidatesUnknown(t *testing.T) {
	_, err := ResolveCandidates(candidates(), []string{"nope"})
	if err == nil || err.Error() != "no candidate nope in pending-onboarding" {
		t.Fatalf("err = %v", err)
	}
}

// pagedCandidates serves n candidates, size a page (the server's own), recording the queries.
func pagedCandidates(t *testing.T, n, size int, queries *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*queries = append(*queries, r.URL.RawQuery)
		io.ReadAll(r.Body)
		from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		to := min(from+size, n)
		var rows []string
		for i := from; i < to; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"id-%d","status":"pending-onboarding","extra":%d}`, i, i))
		}
		cursor := "null"
		if to < n {
			cursor = strconv.Quote(strconv.Itoa(to))
		}
		io.WriteString(w, `{"candidates":[`+strings.Join(rows, ",")+`],"cursor":`+cursor+`}`)
	}
}

func TestListCandidatesReadsEveryPage(t *testing.T) {
	var q []string
	c := newTestClient(t, pagedCandidates(t, 5, 2, &q))
	got, err := ListCandidates(context.Background(), c, "pending-onboarding", "111111111111", "eu-west-1", 0)
	if err != nil || len(got) != 5 || len(q) != 3 {
		t.Fatalf("got %d, %d requests, %v", len(got), len(q), err)
	}
	if !strings.Contains(q[0], "status=pending-onboarding") || !strings.Contains(q[0], "account=111111111111") || !strings.Contains(q[0], "region=eu-west-1") || !strings.Contains(q[0], "limit=200") {
		t.Errorf("first query %q", q[0])
	}
	if !strings.Contains(q[1], "cursor=2") {
		t.Errorf("second query %q", q[1])
	}
	if b, _ := got[4].MarshalJSON(); string(b) != `{"id":"id-4","status":"pending-onboarding","extra":4}` {
		t.Errorf("row not kept raw: %s", b)
	}
}

func TestListCandidatesStopsAtLimit(t *testing.T) {
	var q []string
	c := newTestClient(t, pagedCandidates(t, 9, 2, &q))
	got, err := ListCandidates(context.Background(), c, "pending-onboarding", "", "", 3)
	if err != nil || len(got) != 3 || len(q) != 2 {
		t.Fatalf("got %d, %d requests, %v", len(got), len(q), err)
	}
}
