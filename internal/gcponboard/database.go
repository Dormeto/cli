package gcponboard

// The database step: the router's Firestore database dmtr-<region>, Native mode, in the router's region, without delete
// protection or point-in-time recovery, created by an operation; the install record is written in it as soon as it
// exists.

import (
	"net/http"
	"net/url"
	"time"
)

const (
	firestoreAPI    = "firestore.googleapis.com"
	firestoreV1     = "https://firestore.googleapis.com/v1/"
	databaseTimeout = 10 * time.Minute
)

func (x *run) databaseURL() string {
	return firestoreV1 + "projects/" + esc(x.n.project) + "/databases/" + esc(x.n.database)
}

func (x *run) databaseName() string { return "projects/" + x.n.project + "/databases/" + x.n.database }

// planDatabase follows loadRecord, which read the database.
func (x *run) planDatabase() (*step, error) {
	if x.database {
		x.found("database", x.databaseName())
		return nil, nil
	}
	what := x.n.database + " (Firestore Native, " + x.n.region + ")"
	return &step{name: "database", lines: []string{"+ database " + what}, apply: func() (string, error) {
		// The cache's, as the database does not hold the record yet.
		if err := x.recordFirst("database", x.databaseName()); err != nil {
			return "", err
		}
		var op longOperation
		in := map[string]any{"type": "FIRESTORE_NATIVE", "locationId": x.n.region,
			"deleteProtectionState": "DELETE_PROTECTION_DISABLED", "pointInTimeRecoveryEnablement": "POINT_IN_TIME_RECOVERY_DISABLED"}
		u := firestoreV1 + "projects/" + esc(x.n.project) + "/databases?databaseId=" + url.QueryEscape(x.n.database)
		if err := x.call(http.MethodPost, u, in, &op); err != nil {
			return "", err
		}
		if err := x.wait(firestoreV1, op, databaseTimeout); err != nil {
			return "", err
		}
		x.database = true
		if err := x.writeRecord(); err != nil {
			return "", err
		}
		return "created " + what + ", wrote " + recordPath, nil
	}}, nil
}
