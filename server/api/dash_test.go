package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"chipkey/api"
)

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardStateFollowsTheDemo(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(2))
	p.Enrol()
	p.Login()
	p.Pay(500, "chai@upi")
	su, _ := p.Pay(25000, "shop@upi")
	honest := p.PayPayload(su.TxnID, su.Nonce, 25000, "shop@upi")
	_, sig := p.Sign(honest)
	forged := strings.Replace(string(honest), "shop@upi", "evil@upi", 1)
	payload, _ := p.Sign([]byte(forged)) // base64 of the forged bytes, signature discarded
	if err := p.Confirm(su.TxnID, payload, sig); err == nil {
		t.Fatal("forged payment accepted")
	}

	var st struct {
		Stats   map[string]any      `json:"stats"`
		Devices []map[string]any    `json:"devices"`
		Tokens  []map[string]string `json:"tokens"`
		Nonces  []map[string]string `json:"nonces"`
		Txns    []map[string]string `json:"txns"`
		Policy  map[string]string   `json:"policy"`
	}
	getJSON(t, w.url+"/v1/dash/state", &st)

	if st.Stats["devices"] != 1.0 || st.Stats["sessions"] != 1.0 || st.Stats["paid"] != 1.0 || st.Stats["blocked"] != 1.0 {
		t.Errorf("stats = %v", st.Stats)
	}
	if d := st.Devices[0]; d["tier"] != "STRONGBOX" || d["limits"] != "FULL" || d["verifiedBoot"] != true || d["balance"] != "₹99,500" {
		t.Errorf("device row = %v", d)
	}
	if len(st.Txns) != 2 || st.Txns[0]["status"] != "BLOCKED" || st.Txns[1]["status"] != "PAID" || st.Txns[1]["auth"] != "TOKEN" {
		t.Errorf("transactions = %v", st.Txns)
	}
	if st.Policy["threshold"] != "₹2,000" {
		t.Errorf("policy = %v", st.Policy)
	}

	// Secrets appear as prefixes only.
	if tok := st.Tokens[0]["token"]; !strings.HasSuffix(tok, "…") || !strings.HasPrefix(p.Token, strings.TrimSuffix(tok, "…")) || len(tok) > 12 {
		t.Errorf("token column leaks or mismatches: %q", tok)
	}
	statuses := map[string]int{}
	for _, n := range st.Nonces {
		statuses[n["purpose"]+":"+n["status"]]++
		if len(n["nonce"]) > 12 {
			t.Errorf("nonce column leaks: %q", n["nonce"])
		}
	}
	if statuses["enrol:USED"] != 1 || statuses["session.login:USED"] != 1 || statuses["upi.pay:PENDING"] != 1 {
		t.Errorf("nonce statuses = %v", statuses)
	}
}

func TestDashboardStreamsDecisionsAndEvidence(t *testing.T) {
	w := start(t, nil)
	p := w.phone(chip(1))
	p.Enrol()
	p.Login()

	resp, err := http.Get(w.url + "/v1/dash/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	var events []api.Event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e api.Event
		if err := json.Unmarshal([]byte(line[6:]), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
		if strings.Contains(e.Msg, "X-Token") && strings.Contains(e.Msg, "issued") {
			break // the backlog has caught up to the login
		}
	}

	var attestation, signature *api.Detail
	for i, e := range events {
		if e.ID != int64(i+1) || e.Boot == 0 {
			t.Fatalf("event %d has id %d boot %d", i, e.ID, e.Boot)
		}
		if e.Detail != nil && e.Detail.Kind == "attestation" {
			attestation = e.Detail
		}
		if e.Detail != nil && e.Detail.Kind == "signature" {
			signature = e.Detail
		}
	}
	if events[0].Stage != "BOOT" {
		t.Errorf("first event = %+v", events[0])
	}
	if attestation == nil || !attestation.OK || attestation.Verdict != "TRUSTED · TIER TEE" || len(attestation.Chain) != 3 {
		t.Fatalf("attestation evidence = %+v", attestation)
	}
	if signature == nil || !signature.OK || !strings.Contains(signature.Payload, `"purpose": "session.login"`) {
		t.Fatalf("signature evidence = %+v", signature)
	}
}

func TestDashboardPageIsServedAndSelfContained(t *testing.T) {
	w := start(t, nil)
	resp, err := http.Get(w.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if resp.StatusCode != 200 || !strings.Contains(page, "EVIDENCE INSPECTOR") {
		t.Fatalf("dashboard not served: %d", resp.StatusCode)
	}
	// It has to work on a stage with no network.
	for _, external := range []string{"http://", "https://", "//cdn"} {
		if strings.Contains(page, external) {
			t.Errorf("dashboard references an external resource: %q", external)
		}
	}
}
