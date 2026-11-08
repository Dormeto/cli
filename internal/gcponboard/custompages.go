package gcponboard

// The router's custom waiting pages switch (`dormeto gcp install --custom-pages`, `--custom-pages=false`): written as
// DMT_CUSTOM_PAGES=1 on both Cloud Run services and in the install record (customPages), absent when off. Custom HTML runs
// on the application's address, and on Google Cloud anyone who can invoke the controller is an operator, so the pages are
// off unless an administrator's install turns them on (vision design 2026-11-08 §5.2). Off, the router refuses a custom
// page and serves none already stored. The router reads exactly "1" (router gcp/src/pages.mjs).

import (
	"fmt"
	"slices"
)

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
//
// The switch fails closed. Turning it off is recorded at once: the apply's first write of the record, before either
// service is written, says off, so a run stopped between the controller and the waker is finished by any later run, with
// or without the flag, instead of turning the written one back on. Turning it on is recorded after the services
// (noteImage): stopped between them, it stays off. A dry run or a declined plan writes no record.
func (x *run) useCustomPages() error {
	x.customPages = x.rec.CustomPages
	if x.r.CustomPages != nil {
		x.customPages = *x.r.CustomPages
	}
	if flag := x.customPagesFlag(); flag != "" && x.image == "" {
		return &stop{2, fmt.Sprintf("%s: changes the router's two Cloud Run services, and no router is installed in %s: give --version or --image.\nNothing was changed.", flag, x.n.region)}
	}
	if x.rec.CustomPages && !x.customPages {
		x.rec.CustomPages = false
		x.recChanged = true
	}
	return nil
}

// movesCustomPages tells that writing a service changes its DMT_CUSTOM_PAGES: one created with the switch on, or one whose
// variable is not the switch's (got is nil for a service that does not exist).
func (x *run) movesCustomPages(s serviceSettings, got *runService) bool {
	if got == nil {
		return x.customPages
	}
	return slices.Contains(envDifferences(*got, x.wantService(s)), customPagesEnv)
}

// customPagesLines are the plan's lines of the switch the services are given: on, whenever they are written; off, when
// this run takes the variable from either of them.
func (x *run) customPagesLines(moved bool) []string {
	switch {
	case x.customPages:
		return []string{customPagesOnLine}
	case moved:
		return []string{customPagesOffLine}
	}
	return nil
}
