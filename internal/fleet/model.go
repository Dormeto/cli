package fleet

import "encoding/json"

// Router is a row of GET /iam/routers: what the CLI reads of it. The row as the API sent it is kept, so --json prints
// every field, not only these.
type Router struct {
	Name           string         `json:"name"`
	Account        string         `json:"account,omitempty"` // absent for a router of the manager's own account written in Terraform
	Region         string         `json:"region"`
	Source         string         `json:"source"`            // manager or terraform
	Version        string         `json:"version,omitempty"` // "" when unknown (null)
	Health         Health         `json:"health"`
	Workloads      int            `json:"workloads"`
	WorkloadStates map[string]int `json:"workloadStates,omitempty"`
	Error          string         `json:"error,omitempty"` // the router did not answer
	Operation      *OpRef         `json:"operation,omitempty"`

	raw json.RawMessage
}

// Health is a router's health as the manager reports it ("" when unknown).
type Health struct {
	Status string `json:"status"`
}

// OpRef is the install, upgrade or uninstall running on a router.
type OpRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type routerFields Router // without the methods below, so that decoding does not recurse

func (r *Router) UnmarshalJSON(data []byte) error {
	var f routerFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*r = Router(f)
	r.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON is the row as the API sent it, or the fields known when it was built here.
func (r Router) MarshalJSON() ([]byte, error) {
	if r.raw != nil {
		return r.raw, nil
	}
	return json.Marshal(routerFields(r))
}

// Routers is the answer of GET /iam/routers: the rows and the version available ("" when the manager names none).
type Routers struct {
	Rows      []Router
	Available string
}

func (rs *Routers) UnmarshalJSON(data []byte) error {
	var a struct {
		Routers   []Router `json:"routers"`
		Available *struct {
			Version string `json:"version"`
		} `json:"available"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	rs.Rows, rs.Available = a.Routers, ""
	if a.Available != nil {
		rs.Available = a.Available.Version
	}
	return nil
}

// MarshalJSON is the API's shape: available is absent when none is named.
func (rs Routers) MarshalJSON() ([]byte, error) {
	rows := rs.Rows
	if rows == nil {
		rows = []Router{}
	}
	a := struct {
		Routers   []Router `json:"routers"`
		Available *struct {
			Version string `json:"version"`
		} `json:"available,omitempty"`
	}{Routers: rows}
	if rs.Available != "" {
		a.Available = &struct {
			Version string `json:"version"`
		}{rs.Available}
	}
	return json.Marshal(a)
}

// Workload is a row of GET /iam/workloads. StateAt is when its state began; UpdatedAt the last change of any kind.
type Workload struct {
	WorkloadID string            `json:"workloadId"`
	Name       string            `json:"name,omitempty"`
	Router     string            `json:"router"`
	Account    string            `json:"account,omitempty"`
	Region     string            `json:"region,omitempty"`
	State      string            `json:"state"`
	Tags       map[string]string `json:"tags,omitempty"`
	StateAt    string            `json:"stateAt,omitempty"`
	UpdatedAt  string            `json:"updatedAt,omitempty"`
}

// RouterError is a router that did not answer the workloads' listing.
type RouterError struct {
	Router  string `json:"router"`
	Message string `json:"message"`
}

// Workloads is the answer of GET /iam/workloads.
type Workloads struct {
	Rows    []Workload      `json:"workloads"`
	Total   int             `json:"total"`
	ByState map[string]int  `json:"byState,omitempty"`
	Cursor  json.RawMessage `json:"cursor,omitempty"` // null when the listing is complete
	Errors  []RouterError   `json:"errors,omitempty"`
}

// More tells whether the answer is a page of a longer listing.
func (w Workloads) More() bool {
	c := string(w.Cursor)
	return c != "" && c != "null"
}
