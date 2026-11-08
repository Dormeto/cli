package gcponboard

// The router's custom waiting pages switch (`dormeto gcp install --custom-pages`, `--custom-pages=false`): written as
// DMT_CUSTOM_PAGES=1 on both Cloud Run services and in the install record (customPages), absent when off. Custom HTML runs
// on the application's address, and on Google Cloud anyone who can invoke the controller is an operator, so the pages are
// off unless an administrator's install turns them on (vision design 2026-11-08 §5.2). Off, the router refuses a custom
// page and serves none already stored. The router reads exactly "1" (router gcp/src/pages.mjs).

import "fmt"

const (
	customPagesEnv = "DMT_CUSTOM_PAGES"

	customPagesOnLine  = "custom pages on: a custom waiting page's HTML is served on its application's address"
	customPagesOffLine = "custom pages off: a custom waiting page is refused, and one already stored is not served"
)

// customPagesFlag is the flag that sets the switch, as given: "" when not given.
func (x *run) customPagesFlag() string {
	switch {
	case x.r.CustomPages == nil:
		return ""
	case *x.r.CustomPages:
		return "--custom-pages"
	}
	return "--custom-pages=false"
}

// useCustomPages sets the run's switch: --custom-pages's, else the record's. The flag changes the two services, so it needs
// the router this run installs (useRouter), as the IP lists' flags do: with the access only, the run is refused before any
// change. Follows useRouter.
func (x *run) useCustomPages() error {
	x.customPages = x.rec.CustomPages
	if x.r.CustomPages != nil {
		x.customPages = *x.r.CustomPages
	}
	if flag := x.customPagesFlag(); flag != "" && x.image == "" {
		return &stop{2, fmt.Sprintf("%s: changes the router's two Cloud Run services, and no router is installed in %s: give --version or --image.\nNothing was changed.", flag, x.n.region)}
	}
	return nil
}

// customPagesLines are the plan's lines of the switch the services are given: on, or off when this run's flag turns it off.
func (x *run) customPagesLines() []string {
	switch {
	case x.customPages:
		return []string{customPagesOnLine}
	case x.r.CustomPages != nil:
		return []string{customPagesOffLine}
	}
	return nil
}
