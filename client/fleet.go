package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// RunFleet is the entry point for "shelley fleet [args...]".
func RunFleet(args []string) {
	fs := flag.NewFlagSet("fleet", flag.ExitOnError)
	urlFlag := fs.String("url", defaultClientURL(), "Server URL (unix:///path, http://host:port)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: shelley fleet [-url URL] <subcommand> [args...]

Subcommands:
  status          This node's id, address and peers
  addr            Print this node's address (give it to "join" on other nodes)
  join ADDR       Join the fleet that ADDR belongs to
  ls [PREFIX]     List entries
  get KEY         Print an entry's value
  put KEY [JSON]  Write an entry (JSON from argument or stdin)
  rm KEY          Delete an entry
`)
	}
	fs.Parse(args)
	cc := &clientConfig{serverURL: *urlFlag}
	sub := fs.Args()
	if len(sub) == 0 {
		fs.Usage()
		os.Exit(1)
	}
	arg := func(i int) string {
		if len(sub) <= i {
			fs.Usage()
			os.Exit(1)
		}
		return sub[i]
	}
	var status int
	var body []byte
	switch sub[0] {
	case "status":
		status, body = cc.fleetDo("GET", "/api/fleet", nil)
	case "addr":
		status, body = cc.fleetDo("GET", "/api/fleet", nil)
		if status == 200 {
			var v struct{ Addr string }
			json.Unmarshal(body, &v)
			body = []byte(v.Addr + "\n")
		}
	case "join":
		b, _ := json.Marshal(map[string]string{"addr": arg(1)})
		status, body = cc.fleetDo("POST", "/api/fleet/join", b)
	case "ls":
		prefix := ""
		if len(sub) > 1 {
			prefix = sub[1]
		}
		status, body = cc.fleetDo("GET", "/api/fleet/kv?prefix="+prefix, nil)
	case "get":
		status, body = cc.fleetDo("GET", "/api/fleet/kv/"+arg(1), nil)
	case "put":
		var v []byte
		if len(sub) > 2 {
			v = []byte(sub[2])
		} else {
			v, _ = io.ReadAll(os.Stdin)
		}
		status, body = cc.fleetDo("PUT", "/api/fleet/kv/"+arg(1), v)
	case "rm":
		status, body = cc.fleetDo("DELETE", "/api/fleet/kv/"+arg(1), nil)
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", sub[0])
		fs.Usage()
		os.Exit(1)
	}
	if status/100 != 2 {
		fmt.Fprintf(os.Stderr, "Error: %d %s", status, body)
		os.Exit(1)
	}
	os.Stdout.Write(body)
}

func (cc *clientConfig) fleetDo(method, path string, body []byte) (int, []byte) {
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req, err := cc.newRequest(method, baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("X-Shelley-Request", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		out = []byte("not found\n")
	}
	return resp.StatusCode, out
}
