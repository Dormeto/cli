package fleet

// The manager's Google Cloud connection (GET|PUT|DELETE /iam/discovery/google, Google Cloud manager design §4.3): the record
// `dormeto gcp connect` writes and `dormeto gcp disconnect` reads back and clears. The manager registers the routers it
// names at once.

import "context"

// GoogleConnection is the manager's record of its Google Cloud connection: the host project and its number, the workload
// identity pool and provider IDs, the scopes, the two service accounts' emails, the routers connected, the bindings Google
// added during the connects, and the last connect's time, gcloud account and CLI version.
type GoogleConnection struct {
	Project         string                `json:"project"`
	ProjectNumber   string                `json:"projectNumber"`
	Pool            string                `json:"pool"`
	Provider        string                `json:"provider"`
	Scopes          []string              `json:"scopes"`
	ServiceAccounts GoogleServiceAccounts `json:"serviceAccounts"`
	Routers         []GoogleRouter        `json:"routers"`
	GoogleAdded     []string              `json:"googleAdded"`
	ConnectedAt     string                `json:"connectedAt"`
	ConnectedBy     string                `json:"connectedBy"`
	CLI             string                `json:"cli"`
}

// GoogleServiceAccounts are the emails of the manager's two service accounts, the twins of DmtDiscoveryRole and
// DmtCommandRole.
type GoogleServiceAccounts struct {
	Discovery string `json:"discovery"`
	Command   string `json:"command"`
}

// GoogleRouter is a router of the connection: its project and region, its controller's URL and its version (DMT_VERSION),
// each left out when unknown.
type GoogleRouter struct {
	Project    string `json:"project"`
	Region     string `json:"region"`
	Controller string `json:"controller,omitempty"`
	Version    string `json:"version,omitempty"`
}

type googleAnswer struct {
	Connection *GoogleConnection `json:"connection"`
}

// GoogleConnection is the manager's record, nil when it has none.
func (c *Client) GoogleConnection(ctx context.Context) (*GoogleConnection, error) {
	var out googleAnswer
	if err := c.Get(ctx, "/discovery/google", nil, &out); err != nil {
		return nil, err
	}
	return out.Connection, nil
}

// PutGoogleConnection writes the record and gives it back as the manager stored it.
func (c *Client) PutGoogleConnection(ctx context.Context, rec GoogleConnection) (*GoogleConnection, error) {
	var out googleAnswer
	if err := c.Put(ctx, "/discovery/google", rec, &out); err != nil {
		return nil, err
	}
	return out.Connection, nil
}

// DeleteGoogleConnection clears the record; the manager unregisters its Google Cloud routers.
func (c *Client) DeleteGoogleConnection(ctx context.Context) error {
	return c.Delete(ctx, "/discovery/google", nil)
}
