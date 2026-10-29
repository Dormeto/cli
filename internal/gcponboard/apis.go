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
		var op longOperation
		in := map[string]any{"serviceIds": batch}
		if err := x.call(http.MethodPost, serviceUsage+esc(x.number)+"/services:batchEnable", in, &op); err != nil {
			return "", err
		}
		x.turnedOn = true
		if err := x.wait("https://serviceusage.googleapis.com/v1/", op, apisTimeout); err != nil {
			return "", err
		}
		for _, api := range batch {
			x.rec.addResource("api", api)
		}
	}
	if err := x.policyChanged("apis"); err != nil {
		return "", err
	}
	return "turned on " + strings.Join(x.turningOn, ", "), nil
}
