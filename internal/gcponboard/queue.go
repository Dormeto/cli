package gcponboard

// The queue step: the router's Cloud Tasks queue dmtr-<region> (3 attempts, backoff 5 to 60 s, 50 dispatches a second,
// 100 at once), created when missing and read back (Google may keep a deleted queue's name up to 3 days), its retry and
// rate settings updated when they differ; then roles/cloudtasks.enqueuer on the queue to the router.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Dormeto/cli/internal/gcp"
)

const (
	tasksAPI = "cloudtasks.googleapis.com"

	queueAttempts    = 3
	queueMinBackoff  = "5s"
	queueMaxBackoff  = "60s"
	queueRate        = 50
	queueConcurrency = 100

	queueSettings     = "3 attempts, backoff 5 to 60 s"
	queueAllSettings  = queueSettings + ", 50 dispatches a second, 100 at once"
	queueUpdateFields = "retryConfig,rateLimits"
)

type googleQueue struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	RetryConfig struct {
		MaxAttempts int    `json:"maxAttempts"`
		MinBackoff  string `json:"minBackoff"`
		MaxBackoff  string `json:"maxBackoff"`
	} `json:"retryConfig"`
	RateLimits struct {
		MaxDispatchesPerSecond  float64 `json:"maxDispatchesPerSecond"`
		MaxConcurrentDispatches int     `json:"maxConcurrentDispatches"`
	} `json:"rateLimits"`
}

func (x *run) queueName() string {
	return "projects/" + x.n.project + "/locations/" + x.n.region + "/queues/" + x.n.queue
}

func (x *run) queuesURL() string {
	return "https://cloudtasks.googleapis.com/v2/projects/" + esc(x.n.project) + "/locations/" + esc(x.n.region) + "/queues"
}

// wantQueue is the queue as the install writes it.
func (x *run) wantQueue() map[string]any {
	return map[string]any{
		"name":        x.queueName(),
		"retryConfig": map[string]any{"maxAttempts": queueAttempts, "minBackoff": queueMinBackoff, "maxBackoff": queueMaxBackoff},
		"rateLimits":  map[string]any{"maxDispatchesPerSecond": queueRate, "maxConcurrentDispatches": queueConcurrency},
	}
}

func sameDuration(got, want string) bool {
	g, err := time.ParseDuration(got)
	w, _ := time.ParseDuration(want)
	return err == nil && g == w
}

// queueAsWanted tells whether a queue has the install's settings.
func queueAsWanted(q googleQueue) bool {
	return q.RetryConfig.MaxAttempts == queueAttempts && sameDuration(q.RetryConfig.MinBackoff, queueMinBackoff) &&
		sameDuration(q.RetryConfig.MaxBackoff, queueMaxBackoff) && q.RateLimits.MaxDispatchesPerSecond == queueRate &&
		q.RateLimits.MaxConcurrentDispatches == queueConcurrency
}

func (x *run) planQueue() (*step, error) {
	var got googleQueue
	found, err := x.get(tasksAPI, x.queuesURL()+"/"+esc(x.n.queue), &got)
	if err != nil {
		return nil, err
	}
	var lines []string
	action := ""
	switch {
	case !found:
		action = "create"
		lines = append(lines, "+ queue "+x.n.queue+" ("+queueSettings+")")
	case !queueAsWanted(got):
		action = "update"
		lines = append(lines, "~ queue "+x.n.queue+": "+queueAllSettings)
	default:
		x.found("queue", x.queueName())
	}
	g := x.grantOn("queue")
	rp := QueuePolicy(x.n.project, x.n.region, x.n.queue)
	grant := true
	if found {
		p, err := x.readPolicy(rp)
		if err != nil {
			return nil, err
		}
		if holds(p, g.Grant) {
			grant = false
			x.foundGrant(g)
		}
	}
	if grant {
		lines = append(lines, "+ grant "+x.describe(g.Grant, g.On))
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &step{name: "queue", lines: lines, apply: func() (string, error) {
		var done string
		switch action {
		case "create":
			if err := x.call(http.MethodPost, x.queuesURL(), x.wantQueue(), nil); err != nil {
				return "", err
			}
			// A name deleted lately: the create can appear to succeed, and no queue exists.
			if err := x.call(http.MethodGet, x.queuesURL()+"/"+esc(x.n.queue), nil, nil); gcp.IsNotFound(err) {
				return "", fmt.Errorf("queue %s does not exist after its creation: Google may keep a deleted queue's name blocked for up to 3 days, and a create can appear to succeed in that window", x.n.queue)
			} else if err != nil {
				return "", err
			}
			done = "created " + x.n.queue + " (" + queueSettings + ")"
		case "update":
			u := x.queuesURL() + "/" + esc(x.n.queue) + "?updateMask=" + queueUpdateFields
			if err := x.call(http.MethodPatch, u, x.wantQueue(), nil); err != nil {
				return "", err
			}
			done = "updated " + x.n.queue + " (" + queueAllSettings + ")"
		}
		if action != "" {
			x.rec.addResource("queue", x.queueName())
		}
		if !grant {
			return done, nil
		}
		if err := x.bind(rp, g); err != nil {
			return "", err
		}
		if done == "" {
			return "granted " + g.Role + " on queue " + x.n.queue, nil
		}
		return done + ", granted " + g.Role + " on it", nil
	}}, nil
}
