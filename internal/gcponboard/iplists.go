package gcponboard

// The router's IP lists (`dormeto gcp install --ip-list name=<n>,url=<u>[,refresh=<hours>]`, `--no-ip-lists`): written
// as DMT_IP_LISTS on both Cloud Run services and in the install record. Only the action ignore exists on Google Cloud
// (design §5): in the shared classifier, defer means "only the waiting page's probe wakes", and here every wake already
// is the probe's. A list's name becomes a Firestore document ID, hence its pattern.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultRefreshHours = 24
	ipListAction        = "ignore"
	ipListsEnv          = "DMT_IP_LISTS"
	// withListsMemory is the waker's memory when a list is set: 200,000 entries are held in it.
	withListsMemory = "512Mi"
)

var ipListName = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// IPList is one list of --ip-list; RefreshHours is how often the router downloads it again.
type IPList struct {
	Name, URL    string
	RefreshHours int
}

// RecordIPList is a list as the install record and the router's DMT_IP_LISTS hold it.
type RecordIPList struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Action       string `json:"action"`
	RefreshHours int    `json:"refreshHours"`
}

// ParseIPList reads the value of one --ip-list. An error is a bad flag (exit 2).
func ParseIPList(s string) (IPList, error) {
	l := IPList{RefreshHours: defaultRefreshHours}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return IPList{}, fmt.Errorf("--ip-list: %q is not key=value: name=<n>,url=<u>[,refresh=<hours>] (a comma in a URL is written %%2C)", part)
		}
		if seen[key] {
			return IPList{}, fmt.Errorf("--ip-list: %s is given twice", key)
		}
		seen[key] = true
		switch key {
		case "name":
			l.Name = value
		case "url":
			l.URL = value
		case "refresh":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return IPList{}, fmt.Errorf("--ip-list: refresh %q is not a positive whole number of hours", value)
			}
			l.RefreshHours = n
		case "action":
			return IPList{}, fmt.Errorf("--ip-list: action is not a choice on Google Cloud: only ignore exists, and it is the one written. Every wake there already is the waiting page's probe, so defer would mean nothing: a listed address's GET /_dormeto/wake never wakes, and an explicit POST /_dormeto/wake still does")
		default:
			return IPList{}, fmt.Errorf("--ip-list: %s is not a key: name, url, refresh", key)
		}
	}
	if l.Name == "" {
		return IPList{}, fmt.Errorf("--ip-list: name is needed: name=<n>,url=<u>[,refresh=<hours>]")
	}
	if l.URL == "" {
		return IPList{}, fmt.Errorf("--ip-list %s: url is needed: name=<n>,url=<u>[,refresh=<hours>]", l.Name)
	}
	return l, checkIPList(l)
}

// checkIPList refuses a list the router cannot use, or that must not go into a Cloud Run environment and the record.
func checkIPList(l IPList) error {
	if !ipListName.MatchString(l.Name) {
		return fmt.Errorf("--ip-list: name %q: lowercase letters, digits and - only, 1 to 40 characters (it becomes a document ID)", l.Name)
	}
	if l.RefreshHours <= 0 {
		return fmt.Errorf("--ip-list %s: refresh %d is not a positive whole number of hours", l.Name, l.RefreshHours)
	}
	u, err := url.Parse(l.URL)
	switch {
	case err != nil:
		return fmt.Errorf("--ip-list %s: the URL %q is not one: %v", l.Name, l.URL, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("--ip-list %s: the URL %q is not http or https: the router downloads lists over HTTP(S) only on Google Cloud", l.Name, l.URL)
	case u.Host == "":
		return fmt.Errorf("--ip-list %s: the URL %q has no host", l.Name, l.URL)
	case u.User != nil:
		return fmt.Errorf("--ip-list %s: the URL carries credentials (user:password@): they would go in the install record and in a Cloud Run environment; use a URL that needs none", l.Name)
	}
	return nil
}

// CheckIPLists refuses a bad set of lists, and --ip-list with --no-ip-lists.
func CheckIPLists(lists []IPList, none bool) error {
	if none && len(lists) > 0 {
		return fmt.Errorf("--ip-list and --no-ip-lists: one or the other")
	}
	var names []string
	for _, l := range lists {
		if err := checkIPList(l); err != nil {
			return err
		}
		if slices.Contains(names, l.Name) {
			return fmt.Errorf("--ip-list: the name %s is given twice", l.Name)
		}
		names = append(names, l.Name)
	}
	return nil
}

func recordLists(lists []IPList) []RecordIPList {
	var out []RecordIPList
	for _, l := range lists {
		out = append(out, RecordIPList{Name: l.Name, URL: l.URL, Action: ipListAction, RefreshHours: l.RefreshHours})
	}
	return out
}

func listsOfRecord(lists []RecordIPList) []IPList {
	var out []IPList
	for _, l := range lists {
		out = append(out, IPList{Name: l.Name, URL: l.URL, RefreshHours: l.RefreshHours})
	}
	return out
}

// ipListsValue is DMT_IP_LISTS: the JSON the router's parseLists reads.
func ipListsValue(lists []IPList) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a URL stays readable
	_ = enc.Encode(recordLists(lists))
	return strings.TrimSpace(b.String())
}

// useIPLists sets the run's lists: --ip-list's, none with --no-ip-lists, else the record's. Either flag changes the two
// services, so it needs the router: without --image the recorded image is reused (and, as with --image, its permissions
// tested); with neither, the run is refused, before any change. Follows loadRecord.
func (x *run) useIPLists() error {
	switch {
	case len(x.r.IPLists) > 0:
		x.lists = x.r.IPLists
	case x.r.NoIPLists:
		x.lists = nil
	default:
		x.lists = listsOfRecord(x.rec.IPLists)
	}
	if (len(x.r.IPLists) == 0 && !x.r.NoIPLists) || x.r.Image != "" {
		return nil
	}
	flag := "--ip-list"
	if x.r.NoIPLists {
		flag = "--no-ip-lists"
	}
	if x.rec.Image == "" {
		return &stop{2, fmt.Sprintf("%s: changes the router's two Cloud Run services, and no router is installed in %s: give --image, or install the router first.\nNothing was changed.", flag, x.n.region)}
	}
	x.r.Image = x.rec.Image
	_, x.version, _ = splitImage(x.r.Image)
	x.say("Using the recorded image %s.", x.r.Image)
	rep, err := check(x.ctx, x.d, x.r, []string{"install-router"})
	if err != nil {
		return err
	}
	for _, c := range rep.Commands {
		if !c.OK() {
			return &stop{1, fmt.Sprintf("  ✗ %s: missing %s\nNothing was changed.", c.Command, strings.Join(c.Missing, ", "))}
		}
	}
	return nil
}

// listLines are the plan's lines of the lists the router will download.
func (x *run) listLines() []string {
	var out []string
	for _, l := range x.lists {
		out = append(out, fmt.Sprintf("IP list %s: %s, every %d hours, action %s", l.Name, l.URL, l.RefreshHours, ipListAction))
	}
	return out
}
