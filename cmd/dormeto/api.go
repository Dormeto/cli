package main

// `dormeto api <method> <path>`: a signed call to any route of the manager's /iam API, its answer's body on stdout
// (indented when JSON): what the commands do not cover, such as a route a later manager adds. Exit codes: 0 for a 2xx
// answer; 1 for any other ("HTTP <status>" on stderr, the body still on stdout) or when AWS refuses; 2 for a bad
// argument; 128 and the signal's number on a signal.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

var apiMethods = []string{"GET", "POST", "PUT", "DELETE"}

func newAPI(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	var (
		data   string
		params []string
		asJSON bool
	)
	cmd := fleetCommand("api <method> <path>", "Call a route of the manager's API, signed, and print its answer")
	cmd.Long = "Call a route of the manager's API with the profile's signature and print the answer's body (indented when it is\n" +
		"JSON). The method is GET, POST, PUT or DELETE; the path starts with / and is the route's under /iam (an /iam prefix\n" +
		"is dropped), percent-encoded where a segment needs it. --data is the body: JSON, @<file>, or - for standard input.\n" +
		"A GET answered 429 or 5xx is tried up to 3 times in all. Any status but 2xx prints HTTP <status> on stderr, exit 1.\n" +
		"--json changes nothing (the body is the API's answer); it is accepted as every command accepts it.\n\n" +
		"  dormeto api GET /workloads -q state=ASLEEP -q limit=10\n" +
		"  dormeto api POST /routers/prod-eu/pause --data '{\"reason\":\"maintenance\"}'"
	f.bind(cmd)
	cmd.Flags().StringVar(&data, "data", "", "the request's body: JSON, @<file>, or - for standard input")
	cmd.Flags().StringArrayVarP(&params, "query", "q", nil, "a query parameter, key=value (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "the body as the API answers it (what is printed anyway)")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		method, path, query, err := apiRequest(args, params)
		var body []byte
		if err == nil && c.Flags().Changed("data") {
			body, err = readData(c, data)
		}
		if err != nil {
			return finish(c, err)
		}
		return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
			status, answer, err := api.Raw(ctx, method, path, query, body)
			if err != nil {
				return err
			}
			printBody(c.OutOrStdout(), answer)
			if status < 200 || status > 299 {
				fmt.Fprintf(c.ErrOrStderr(), "HTTP %d\n", status)
				return exitCode(1)
			}
			return nil
		})
	}
	return cmd
}

// apiRequest checks the method and path and gives the path under /iam with its query, the -q parameters added.
func apiRequest(args, params []string) (method, path string, query url.Values, err error) {
	if len(args) != 2 {
		return "", "", nil, usagef("give a method and a path: dormeto api <method> <path>")
	}
	method = strings.ToUpper(args[0])
	if !slices.Contains(apiMethods, method) {
		return "", "", nil, usagef("%q is not a method: GET, POST, PUT or DELETE", args[0])
	}
	if !strings.HasPrefix(args[1], "/") {
		return "", "", nil, usagef("%q is not a path: it starts with /", args[1])
	}
	path, raw, _ := strings.Cut(args[1], "?")
	if rest, ok := strings.CutPrefix(path, "/iam"); ok && (rest == "" || rest[0] == '/') {
		path = "/" + strings.TrimPrefix(rest, "/")
	}
	if query, err = url.ParseQuery(raw); err != nil {
		return "", "", nil, usagef("%q: its query cannot be read: %v", args[1], err)
	}
	for _, p := range params {
		key, value, ok := strings.Cut(p, "=")
		if !ok || key == "" {
			return "", "", nil, usagef("-q: %q is not key=value", p)
		}
		query.Add(key, value)
	}
	return method, path, query, nil
}

// readData is the body --data gives: the text itself, @<file>'s content, or standard input's for -.
func readData(c *cobra.Command, data string) ([]byte, error) {
	var b []byte
	var err error
	switch {
	case data == "-":
		b, err = io.ReadAll(c.InOrStdin())
	case strings.HasPrefix(data, "@"):
		b, err = os.ReadFile(strings.TrimPrefix(data, "@"))
	default:
		return []byte(data), nil
	}
	if err != nil {
		return nil, usagef("--data: %v", err)
	}
	return b, nil
}

// printBody prints an answer's body, indented when it is JSON, ending with a newline; nothing for an empty one.
func printBody(w io.Writer, data []byte) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return
	}
	var b bytes.Buffer
	if json.Indent(&b, data, "", "  ") == nil {
		data = b.Bytes()
	}
	fmt.Fprintf(w, "%s\n", data)
}
