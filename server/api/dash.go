package api

import (
	"bytes"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"chipkey/attest"
)

// The dashboard is one self-contained page: no CDN, no build step, so it
// works on a stage with no network.
//
//go:embed dashboard.html
var dashboardHTML []byte

// Event is one line of the decision log, as the dashboard receives it.
type Event struct {
	// Boot identifies this run of the server, so a reconnecting dashboard
	// can tell a restart from a replay.
	Boot   int64   `json:"boot"`
	ID     int64   `json:"id"`
	Time   string  `json:"time"`
	Stage  string  `json:"stage"`
	Mark   string  `json:"mark"`
	Msg    string  `json:"msg"`
	Detail *Detail `json:"detail,omitempty"`
	// Alarm asks the dashboard for its full-screen warning and siren.
	Alarm *Alarm `json:"alarm,omitempty"`
	// Reset tells the dashboard the night has been rewound: clear the screen.
	Reset bool `json:"reset,omitempty"`
	// Clear tells the dashboard everything was wiped: empty the screen, with
	// no rewind splash.
	Clear bool `json:"clear,omitempty"`
}

// Alarm is the text of a full-screen warning.
type Alarm struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
}

// Detail is the evidence behind a decision: a decoded attestation or a
// signed payload with its verdict.
type Detail struct {
	Kind    string      `json:"kind"` // "attestation" or "signature"
	Title   string      `json:"title"`
	OK      bool        `json:"ok"`
	Verdict string      `json:"verdict"`
	Rows    [][2]string `json:"rows"`
	Chain   []string    `json:"chain,omitempty"`
	Payload string      `json:"payload,omitempty"`
}

type bus struct {
	mu   sync.Mutex
	boot int64
	next int64
	ring []Event
	subs map[chan Event]struct{}
}

func newBus() *bus {
	return &bus{boot: time.Now().UnixNano(), subs: map[chan Event]struct{}{}}
}

func (b *bus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	e.Boot, e.ID = b.boot, b.next
	if b.ring = append(b.ring, e); len(b.ring) > 300 {
		b.ring = b.ring[len(b.ring)-300:]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // a stalled dashboard misses lines rather than blocking a payment
		}
	}
}

func (b *bus) subscribe() ([]Event, chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.subs[ch] = struct{}{}
	backlog := append([]Event(nil), b.ring...)
	return backlog, ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.signedIn(r) {
		s.loginPage(w, http.StatusOK, "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(dashboardHTML)
}

// dashEvents streams the decision log as server-sent events.
func (s *Server) dashEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	backlog, ch, cancel := s.bus.subscribe()
	defer cancel()

	send := func(e Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	for _, e := range backlog {
		send(e)
	}
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case e := <-ch:
			send(e)
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case <-r.Context().Done():
			return
		}
		flusher.Flush()
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// dashState is the "database" view: everything the backend remembers.
// Secrets are shown as prefixes only.
func (s *Server) dashState(w http.ResponseWriter, _ *http.Request) {
	type row = map[string]any
	now := s.cfg.Now()

	s.mu.Lock()
	devices := make([]row, 0, len(s.devices))
	ordered := make([]*device, 0, len(s.devices))
	for _, d := range s.devices {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].enrolled.After(ordered[j].enrolled) })
	for _, d := range ordered {
		limits := "FULL"
		if !d.full() {
			limits = Rupees(s.cfg.ReducedLimit)
		}
		publicKey := "none"
		if d.pub != nil {
			pub, _ := x509.MarshalPKIXPublicKey(d.pub)
			publicKey = clip(hex.EncodeToString(pub[len(pub)-65:]), 24)
		}
		name := d.name
		if name == "" {
			name = short(d.id)
		}
		account := "private"
		if !strings.HasPrefix(d.acct, "device:") {
			account = maskPhone(d.acct)
		}
		devices = append(devices, row{
			"device": short(d.id), "name": name, "account": account, "status": strings.ToUpper(d.status),
			"keyId": short(d.keyID), "tier": strings.ToUpper(d.tier), "verifiedBoot": d.integrity,
			"limits": limits, "balance": Rupees(s.balance[d.acct]), "root": d.root,
			"publicKey": publicKey, "enrolled": d.enrolled.Format("15:04:05"),
		})
	}

	type liveToken struct {
		value string
		*token
	}
	var live []liveToken
	for v, t := range s.tokens {
		if now.Before(t.expires) {
			live = append(live, liveToken{v, t})
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].issued.After(live[j].issued) })
	tokens := make([]row, 0, len(live))
	for _, t := range live {
		tokens = append(tokens, row{
			"token": clip(t.value, 8), "device": short(t.deviceID),
			"ttl": t.expires.Sub(now).Round(time.Second).String(), "issued": t.issued.Format("15:04:05"),
		})
	}

	nonces := make([]row, 0, 8)
	for i := len(s.nonceLog) - 1; i >= 0 && len(nonces) < 8; i-- {
		n := s.nonceLog[i]
		status := n.status
		if status == "pending" && now.After(n.expires) {
			status = "expired"
		}
		nonces = append(nonces, row{
			"nonce": clip(n.value, 8), "purpose": n.purpose, "device": short(n.deviceID),
			"status": strings.ToUpper(status), "issued": n.issued.Format("15:04:05"),
		})
	}

	paid := 0
	txns := make([]row, 0, 8)
	for i := len(s.txnLog) - 1; i >= 0; i-- {
		t := s.txnLog[i]
		status := "PENDING"
		switch {
		case t.paid:
			status = "PAID"
			paid++
		case t.lastError != "":
			status = "BLOCKED"
		}
		if len(txns) < 8 {
			txns = append(txns, row{
				"txn": t.id, "amount": Rupees(t.amount), "payee": t.payee, "auth": strings.ToUpper(t.auth),
				"status": status, "note": t.lastError, "at": t.created.Format("15:04:05"),
			})
		}
	}
	s.expireApprovals()
	messages := make([]row, 0, 6)
	for i := len(s.smsLog) - 1; i >= 0 && len(messages) < 6; i-- {
		m := s.smsLog[i]
		status := "DELIVERED"
		if m.forwarded {
			status = "FORWARDED"
		}
		messages = append(messages, row{"at": m.at.Format("15:04:05"), "to": maskPhone(m.to), "text": clip(m.text, 18), "status": status})
	}
	requests := make([]row, 0, 4)
	for i := len(s.approvalLog) - 1; i >= 0 && len(requests) < 4; i-- {
		a := s.approvalLog[i]
		requests = append(requests, row{"id": a.id, "device": a.deviceName, "account": maskPhone(a.phone), "status": strings.ToUpper(a.status), "at": a.created.Format("15:04:05")})
	}
	balance := ""
	if len(ordered) > 0 {
		balance = Rupees(s.balance[ordered[0].acct])
	}
	stats := row{"devices": len(s.devices), "sessions": len(live), "paid": paid, "blocked": s.blocked, "balance": balance}
	mode, forwarding := s.mode, s.forwarding
	s.mu.Unlock()

	reply(w, http.StatusOK, row{
		"stats": stats, "devices": devices, "tokens": tokens, "nonces": nonces, "txns": txns,
		"mode": mode, "forwarding": forwarding, "sms": messages, "approvals": requests,
		"login": s.loginOn(),
		"policy": row{
			"threshold": Rupees(s.cfg.Threshold), "reducedLimit": Rupees(s.cfg.ReducedLimit),
			"tokenTtl": s.cfg.TokenTTL.String(), "package": s.cfg.Package,
		},
	})
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

// attestationDetail turns a decoded chain into the dashboard's evidence card.
func attestationDetail(chain [][]byte, res *attest.Result, trusted bool, tier string, d *device) *Detail {
	det := &Detail{Kind: "attestation", Title: "key attestation · device " + short(d.id), OK: d.full()}
	for i, der := range chain {
		label := fmt.Sprintf("[%d] unreadable", i)
		if c, err := x509.ParseCertificate(der); err == nil {
			name := c.Subject.CommonName
			if name == "" {
				name = c.Subject.String()
			}
			label = fmt.Sprintf("[%d] %s", i, name)
		}
		det.Chain = append(det.Chain, label)
	}
	root := res.RootName
	if !trusted {
		root = "NOT TRUSTED"
	}
	boot, locked := "unknown", "unknown"
	if res.HasRootOfTrust {
		boot, locked = res.BootState.String(), yesNo(res.DeviceLocked)
	}
	det.Rows = [][2]string{
		{"chain ends at", root},
		{"challenge", "matches our nonce"},
		{"key lives in", strings.ToUpper(res.KeyLevel.String())},
		{"attested by", strings.ToUpper(res.AttestationLevel.String())},
		{"verified boot", boot},
		{"bootloader locked", locked},
		{"needs biometric", yesNo(res.UserAuthRequired)},
		{"requested by", strings.Join(res.PackageNames, ",")},
		{"key id", d.keyID},
	}
	switch {
	case d.full():
		det.Verdict = "TRUSTED · TIER " + strings.ToUpper(tier)
	case !trusted:
		det.Verdict = "UNVERIFIABLE · SOFTWARE TIER"
	default:
		det.Verdict = "REDUCED LIMITS · TIER " + strings.ToUpper(tier)
	}
	return det
}

// publishSignature shows the bytes that were signed and what the check said.
func (s *Server) publishSignature(stage, title string, d *device, req signedRequest, err error) {
	det := &Detail{Kind: "signature", Title: title, OK: err == nil, Verdict: "SIGNATURE VALID"}
	raw, _ := base64.StdEncoding.DecodeString(req.Payload)
	sig, _ := base64.StdEncoding.DecodeString(req.Signature)
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		det.Payload = pretty.String()
	} else {
		det.Payload = string(raw)
	}
	reason := "signature and fields check out"
	if err != nil {
		reason = err.Error()
		det.Verdict = "REJECTED"
		if err == errBadSignature {
			det.Verdict = "SIGNATURE INVALID"
		}
	}
	det.Rows = [][2]string{
		{"key id", d.keyID},
		{"device", d.id},
		{"algorithm", "ECDSA P-256 over SHA-256"},
		{"signature", clip(hex.EncodeToString(sig), 28)},
		{"result", reason},
	}
	mark := "ok"
	if err != nil {
		mark = "no"
	}
	e := Event{Time: s.cfg.Now().Format("15:04:05"), Stage: stage, Mark: mark, Msg: "signature checked", Detail: det}
	if err == errBadSignature {
		e.Alarm = &Alarm{Title: "ATTACK BLOCKED", Sub: "the payload changed after the chip signed it"}
	}
	s.bus.publish(e)
}
