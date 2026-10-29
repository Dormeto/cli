package gcponboard

// The apis step: the manifest's APIs that are off, turned on 20 at a time (Service Usage's limit), each batch's
// operation awaited; then the project's policy is read again and Google's bindings of the APIs shown and recorded.

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	enableBatch = 20
	apisTimeout = 5 * time.Minute
)

func (x *run) planAPIs() (*step, error) {
	if len(x.turningOn) == 0 {
		return nil, nil
	}
	var lines []string
	for _, api := range x.turningOn {
		lines = append(lines, "+ API "+api)
	}
	return &step{name: "apis", lines: lines, apply: x.turnOnAPIs}, nil
}

func (x *run) turnOnAPIs() (string, error) {
	for batch := range slices.Chunk(x.turningOn, enableBatch) {
		// Asked for, Google may turn them on even when this run stops waiting: a stopped run records those it finds on,
		// and the next run those on by then.
		x.rec.Enabling = append(x.rec.Enabling, batch...)
		var op longOperation
		in := map[string]any{"serviceIds": batch}
		if err := x.call(http.MethodPost, serviceUsage+esc(x.number)+"/services:batchEnable", in, &op); err != nil {
			return "", err
		}
		x.turnedOn = true
		if err := x.wait("https://serviceusage.googleapis.com/v1/", op, apisTimeout); err != nil {
			return "", err
		}
		x.recordAPIs(batch)
	}
	if err := x.policyChanged("apis"); err != nil {
		return "", err
	}
	return "turned on " + strings.Join(x.turningOn, ", "), nil
}

// recordAPIs records the APIs of the record's Enabling that are on: the install turned them on.
func (x *run) recordAPIs(on []string) {
	var still []string
	for _, api := range x.rec.Enabling {
		if slices.Contains(on, api) {
			x.rec.addResource("api", api)
		} else if !slices.Contains(still, api) {
			still = append(still, api)
		}
	}
	x.rec.Enabling = still
}
