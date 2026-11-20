// Command pocdryrun prints — and optionally fires — the request the refreshed
// OpenCode free-tier fingerprint produces.
//
//	go run ./cmd/pocdryrun                          # print the request, send nothing
//	go run ./cmd/pocdryrun -live                    # actually POST it
//	go run ./cmd/pocdryrun -model muse-spark-1.3    # a Responses-API free model
//	go run ./cmd/pocdryrun -delta                   # print the v1 → v2 delta
//
// The live mode goes out over the direct connection. Firefly itself routes the
// oc upstream through WARP (egress_mode: warp in configs/upstreams.json), so a
// live run from here proves the fingerprint, not the egress path.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	fp "poc/opencode-fingerprint"
)

func main() {
	var (
		live      = flag.Bool("live", false, "actually POST the request instead of only printing it")
		model     = flag.String("model", "deepseek-v4.1-flash", "public model id; resolved through the free-tier alias map")
		message   = flag.String("message", "ping", "user turn to send")
		session   = flag.String("session", "", "session id to reuse; generated when empty")
		baseURL   = flag.String("url", "https://opencode.ai/zen/v1", "zen gateway base URL")
		timeout   = flag.Duration("timeout", 90*time.Second, "request timeout for -live")
		showDelta = flag.Bool("delta", false, "print the v1 → v2 fingerprint delta and exit")
		maxBody   = flag.Int("maxbody", 4096, "bytes of response body to print in -live mode")
	)
	flag.Parse()

	if *showDelta {
		printDelta()
		return
	}

	sessionID := *session
	if sessionID == "" {
		sessionID = fp.GenerateSessionID()
	}

	upstreamModel := fp.MapFreeModel(*model)
	req := fp.BuildFreeRequest(fp.DefaultAppInfo(), sessionID, upstreamModel, *message)
	req.URL = strings.TrimRight(*baseURL, "/") + "/chat/completions"

	body, err := json.MarshalIndent(req.Body, "", "  ")
	if err != nil {
		fatal("marshal body: %v", err)
	}

	fmt.Printf("%s %s\n", req.Method, req.URL)
	for _, hdr := range req.Headers {
		fmt.Printf("%s: %s\n", hdr.Name, hdr.Value)
	}
	fmt.Printf("\n%s\n", body)

	if !*live {
		fmt.Printf("\n(dry run — pass -live to send it)\n")
		return
	}

	httpReq, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(body))
	if err != nil {
		fatal("build request: %v", err)
	}
	for _, hdr := range req.Headers {
		httpReq.Header.Set(hdr.Name, hdr.Value)
	}

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		fatal("send request: %v", err)
	}
	defer resp.Body.Close()

	fmt.Printf("\n--- response ---\nHTTP %s\n", resp.Status)
	for name, values := range resp.Header {
		fmt.Printf("%s: %s\n", name, strings.Join(values, ", "))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(*maxBody)))
	if err != nil {
		fatal("read response: %v", err)
	}
	fmt.Printf("\n%s\n", raw)

	// The 403 we are fixing looks like:
	//   {"type":"FreeTierError","message":"OpenCode's free tier can only be
	//    used from within OpenCode. Download it at https://opencode.ai ..."}
	if resp.StatusCode == http.StatusForbidden {
		fmt.Printf("\nFAIL: still gated. The pinned fingerprint is stale for this release.\n")
		os.Exit(1)
	}
	fmt.Printf("\nOK: the gate accepted the v2 fingerprint.\n")
}

func printDelta() {
	fmt.Printf("%-24s %-58s %-46s\n", "ATTRIBUTE", "V1 (pinned today)", "V2 (v2.0.29)")
	fmt.Println(strings.Repeat("-", 132))
	for _, c := range fp.Delta() {
		fmt.Printf("%-24s %-58s %-46s\n", c.Attribute, c.V1, c.V2)
		fmt.Printf("%-24s   ↳ %s\n\n", "", c.Note)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pocdryrun: "+format+"\n", args...)
	os.Exit(1)
}
