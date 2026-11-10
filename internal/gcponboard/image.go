package gcponboard

// The router's image (design §4): Dormeto's public image by default, <multi-region>-docker.pkg.dev/dormeto-releases/
// router/router:<version>, at the router version the CLI embeds, read without a token and deployed by its digest;
// --version picks another, up to that one; --image is a customer's own copy, checked as before; --access-only installs no
// router. A run with none of them follows the record: a first install takes the default version, an older router of
// Dormeto's is upgraded to it, an image of --image is kept, the access only stays the access only. A router of Dormeto's
// newer than the CLI's stops every run before any change: this CLI's roles and access are older than what it needs.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	cli "github.com/Dormeto/cli"
	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

// imageStep and imageAPI are install-router's step of the image's permissions and Artifact Registry's API: an image of the
// customer's own (--image, given or recorded) needs them, Dormeto's public image neither. The manifest lists both without
// saying so: the condition is the CLI's.
const (
	imageStep = "image"
	imageAPI  = "artifactregistry.googleapis.com"
)

// publicManifestTypes are the media types the HEAD of a public image's manifest asks for.
var publicManifestTypes = []string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
}

// embeddedRouterVersion is the router version of the parity file this CLI embeds (testdata/parity/versions.json,
// written by the manager's export): the version the embedded access manifest describes, the install's default and the
// newest --version takes.
func embeddedRouterVersion() string {
	var v struct {
		Router string `json:"router"`
	}
	_ = json.Unmarshal(cli.Versions, &v)
	return v.Router
}

// DefaultRouterVersion is the install's default router version, the newest --version takes: embeddedRouterVersion.
func DefaultRouterVersion() string { return embeddedRouterVersion() }

// defaultImage is Dormeto's public image of a router version, in the multi-region of the router's region: europe-* uses
// europe; us-*, northamerica-* and southamerica-* use us; every other region uses asia.
func defaultImage(region, version string) string {
	multi := "asia"
	switch prefix, _, _ := strings.Cut(region, "-"); prefix {
	case "europe":
		multi = "europe"
	case "us", "northamerica", "southamerica":
		multi = "us"
	}
	return multi + "-docker.pkg.dev/dormeto-releases/router/router:" + version
}

// byDigest is how the services run an image: Dormeto's public one by its digest, without its tag (<name>@sha256:…, the
// form Google documents; the version stays in the record and DMT_VERSION), an image of --image as given.
func byDigest(image, digest string) string {
	if digest == "" {
		return image
	}
	name, _, _ := splitImage(image)
	return name + "@" + digest
}

// deployed is the image the services run.
func (x *run) deployed() string { return byDigest(x.image, x.digest) }

// deployedImage is the image a record's services run.
func (r Record) deployedImage() string { return byDigest(r.Image, r.Digest) }

// routerFlags is the refusal of flags that contradict one another ("" when none do): --image, --version and
// --access-only each pick the router, and the IP lists' and the custom pages' flags need one.
func (x *run) routerFlags() string {
	var given []string
	for _, f := range []struct {
		name string
		on   bool
	}{{"--image", x.r.Image != ""}, {"--version", x.r.Version != ""}, {"--access-only", x.r.AccessOnly}} {
		if f.on {
			given = append(given, f.name)
		}
	}
	switch {
	case len(given) == 3:
		return "--image, --version and --access-only: one at most"
	case len(given) == 2:
		return given[0] + " and " + given[1] + ": one or the other"
	case x.r.AccessOnly && x.ipListsFlag() != "":
		return x.ipListsFlag() + " and --access-only: one or the other"
	case x.r.AccessOnly && x.customPagesFlag() != "":
		return x.customPagesFlag() + " and --access-only: one or the other"
	}
	return ""
}

// useRouter sets the router this run installs: x.image, x.version and x.digest, none for the access only. It follows
// loadRecord. --image's own checks ran already (checkImage); --version's permissions were tested with install's.
func (x *run) useRouter() error {
	def := embeddedRouterVersion()
	switch v := x.rec.RouterVersion; {
	case x.rec.Digest != "" && install.IsVersion(v) && install.CompareVersions(v, def) > 0:
		return &stop{1, fmt.Sprintf("router %s is newer than this CLI knows (%s): brew upgrade dormeto\nNothing was changed.", v, def)}
	case x.r.Image != "":
		x.setAccessOnly(false)
		return nil
	case x.r.Version != "":
		x.setAccessOnly(false)
		return x.usePublic(x.r.Version)
	case x.r.AccessOnly && x.rec.Image != "":
		return &stop{2, fmt.Sprintf("--access-only: router %s is installed, version %s, and the install removes no router: dormeto gcp routers uninstall removes it with its access.\nNothing was changed.",
			x.n.router, x.rec.RouterVersion)}
	case x.r.AccessOnly:
		x.setAccessOnly(true)
		return nil
	// An older CLI's finished install without an image ran without --image, as the docs said then: the access only. An
	// unfinished one goes on as a first install.
	case x.rec.AccessOnly, x.recorded && !x.resumed && x.rec.Image == "":
		x.notice("the router isn't installed: dormeto gcp routers install --version %s", def)
		return nil
	case x.rec.Image != "" && x.rec.Digest == "":
		return x.keepOwnImage(def)
	}
	if err := x.requireRouter(); err != nil {
		return err
	}
	return x.usePublic(def)
}

// keepOwnImage keeps the recorded image of an earlier --image: its permissions and the image itself are checked as
// --image's are, so that one deleted stops the run before any change. The public version is said to be available, unless
// the image's is newer.
func (x *run) keepOwnImage(def string) error {
	_, tag, _ := splitImage(x.rec.Image)
	x.r.Image, x.image, x.version, x.keepsRouter = x.rec.Image, x.rec.Image, tag, true
	x.say("Using the recorded image %s.", x.image)
	if !install.IsVersion(tag) || install.CompareVersions(tag, def) <= 0 {
		x.notice("router %s is available: dormeto gcp routers install --version %s", def, def)
	}
	if err := x.requireRouter(); err != nil {
		return err
	}
	return x.checkImage()
}

// usePublic takes Dormeto's public image of a version: its digest read with an anonymous HEAD of its manifest. Missing,
// the run stops naming the version; out of reach, it stops too.
func (x *run) usePublic(version string) error {
	image := defaultImage(x.n.region, version)
	name, _, _ := splitImage(image)
	host, path, _ := strings.Cut(name, "/")
	digest, err := x.d.Google.Digest(x.ctx, "https://"+host+"/v2/"+path+"/manifests/"+version, publicManifestTypes...)
	switch {
	case gcp.IsNotFound(err):
		return &stop{1, fmt.Sprintf("  ✗ image: router %s is not in Dormeto's public registry (%s)\nNothing was changed.", version, image)}
	case err != nil:
		return fmt.Errorf("image %s: %w", image, err)
	}
	x.image, x.version, x.digest = image, version, digest
	x.say("Checked: image %s is public, digest %s.", image, digest)
	return nil
}

// requireRouter tests install-router's permissions once the record says the run installs the router (with --image or
// --version, install's test asked them already): a missing one stops the run before any change. An image of the
// customer's own needs Artifact Registry's API too: turned on with the others when it is off.
func (x *run) requireRouter() error {
	rep, err := check(x.ctx, x.d, x.r, []string{"install-router"})
	if err != nil {
		return err
	}
	for _, c := range rep.Commands {
		if !c.OK() {
			return &stop{1, fmt.Sprintf("  ✗ %s: missing %s\nNothing was changed.", c.Command, strings.Join(c.Missing, ", "))}
		}
	}
	for _, api := range rep.APIs.Missing {
		if !slices.Contains(x.turningOn, api) {
			x.turningOn = append(x.turningOn, api)
		}
	}
	return nil
}

// setAccessOnly records whether the install is of the access only.
func (x *run) setAccessOnly(on bool) {
	if x.rec.AccessOnly != on {
		x.rec.AccessOnly = on
		x.recChanged = true
	}
}

// notice says what the run keeps and how to change it, where the install's other "!" lines go: the output.
func (x *run) notice(format string, a ...any) { x.say("  ! "+format, a...) }

// installCommand is the command that installs the router again, at least new enough for a hint: Dormeto's public image
// at the CLI's version, which has what every hint needs; for a router of the customer's own image (--image in the install
// record), --image with image saying what it must be, as they rebuild their copy. A record that cannot be read counts as
// the public image, the default.
func (x *run) installCommand(flags, image string) string {
	rec := x.rec
	if rec == nil {
		read, err := recordInDatabase(x)
		if err != nil {
			read = Record{}
		}
		rec = &read
	}
	if rec.Image != "" && rec.Digest == "" {
		return "dormeto gcp routers install " + flags + " --image " + image
	}
	return "dormeto gcp routers install " + flags + " --version " + embeddedRouterVersion()
}
