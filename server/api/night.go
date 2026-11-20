package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// This file is the story the talk tells. One account, reached by a phone
// number, on two nights.
//
// In ModeLegacy the number is proven by an OTP, and an OTP goes to whoever
// receives the SMS. In ModeChip the account is bound to a key inside one
// phone's secure hardware, and a second phone gets in only if the first one
// signs for it.

const (
	ModeChip   = "chip"
	ModeLegacy = "legacy"

	PurposeApprove = "device.approve"

	statusBound    = "bound"
	statusPending  = "pending"
	statusRejected = "rejected"
	statusLegacy   = "legacy"
)

type sms struct {
	id        int
	to        string
	text      string
	at        time.Time
	forwarded bool
}

type otpCode struct {
	code    string
	expires time.Time
}

// approval is a new phone waiting for the old phone's signature.
type approval struct {
	id         string
	phone      string
	newDevice  string
	askDevice  string
	deviceName string
	tier       string
	keyID      string
	nonce      string
	created    time.Time
	expires    time.Time
	status     string // "pending", "approved", "blocked" or "expired"
}

func modeLabel(mode string) string {
	if mode == ModeLegacy {
		return "OTP ONLY, the app before the chip"
	}
	return "CHIP, hardware-bound keys"
}

func (s *Server) nightRoutes() {
	s.mux.HandleFunc("POST /v1/login/start", s.loginStart)
	s.mux.HandleFunc("POST /v1/login/otp", s.loginOTP)
	s.mux.HandleFunc("GET /v1/sms", s.smsFeed)
	s.mux.HandleFunc("GET /v1/approvals/pending", s.approvalsPending)
	s.mux.HandleFunc("GET /v1/approvals/{id}", s.approvalStatus)
	s.mux.HandleFunc("POST /v1/approvals/{id}/decision", s.approvalDecision)
	s.mux.HandleFunc("POST /v1/recovery/start", s.recoveryStart)
	s.mux.HandleFunc("GET /v1/demo", s.staff(s.demoGet))
	s.mux.HandleFunc("POST /v1/demo", s.staff(s.demoSet))
}

// ---- helpers. Callers hold s.mu unless noted. ------------------------------

// normalizePhone keeps the last ten digits, so "+91 98765 43210" and
// "9876543210" are the same account.
func normalizePhone(in string) string {
	var digits []rune
	for _, r := range in {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return string(digits)
}

func maskPhone(phone string) string {
	if len(phone) < 4 {
		return phone
	}
	return "••" + phone[len(phone)-4:]
}

func (d *device) label() string {
	if d.name != "" {
		return d.name
	}
	return "device " + short(d.id)
}

func (s *Server) ensureAccount(acct string) {
	if _, ok := s.balance[acct]; !ok {
		s.balance[acct] = 100000
	}
}

// boundDevice is the phone whose chip currently holds the account.
func (s *Server) boundDevice(phone string) *device {
	for _, d := range s.devices {
		if d.acct == phone && d.status == statusBound && d.pub != nil {
			return d
		}
	}
	return nil
}

func (s *Server) now() string { return s.cfg.Now().Format("15:04:05") }

// sendOTP "texts" a one-time code to the number. If the courier app is on
// her phone, the same SMS reaches the thief.
func (s *Server) sendOTP(phone string) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	code := fmt.Sprintf("%06d", n.Int64())
	s.otps[phone] = &otpCode{code: code, expires: s.cfg.Now().Add(s.cfg.NonceTTL)}
	s.smsSeq++
	s.smsLog = append(s.smsLog, &sms{
		id: s.smsSeq, to: phone, at: s.cfg.Now(), forwarded: s.forwarding,
		text: code + " is your OTP to log in. Do not share it with anyone.",
	})
	if len(s.smsLog) > 40 {
		s.smsLog = s.smsLog[len(s.smsLog)-40:]
	}
	s.say("SMS", "", "OTP sent to %s", maskPhone(phone))
	if s.forwarding {
		s.say("SMS", "warn", "the same SMS left her phone again, forwarded to another number")
	}
}

func (s *Server) checkOTP(phone, code string) bool {
	o, ok := s.otps[phone]
	if !ok || s.cfg.Now().After(o.expires) || o.code != strings.TrimSpace(code) {
		return false
	}
	delete(s.otps, phone)
	return true
}

func (s *Server) otpFresh(phone, deviceID string) bool {
	at, ok := s.otpPassed[phone+"|"+deviceID]
	return ok && s.cfg.Now().Sub(at) < 5*time.Minute
}

func (s *Server) openApproval(phone string, fresh, holder *device) *approval {
	a := &approval{
		id: "A-" + randomHex(3), phone: phone, newDevice: fresh.id, askDevice: holder.id,
		deviceName: fresh.label(), tier: fresh.tier, keyID: fresh.keyID,
		created: s.cfg.Now(), expires: s.cfg.Now().Add(s.cfg.ApprovalTTL), status: "pending",
	}
	a.nonce = s.issueNonce(holder.id, PurposeApprove, "")
	// The nonce must outlive the approval window.
	if n, ok := s.nonces[a.nonce]; ok {
		n.expires = a.expires.Add(time.Minute)
	}
	s.approvals[a.id] = a
	if s.approvalLog = append(s.approvalLog, a); len(s.approvalLog) > 20 {
		s.approvalLog = s.approvalLog[len(s.approvalLog)-20:]
	}
	return a
}

// refuse ends an approval without letting the new phone in.
func (s *Server) refuse(a *approval, status, why string) {
	a.status = status
	if d, ok := s.devices[a.newDevice]; ok && d.status == statusPending {
		d.status = statusRejected
	}
	s.blocked++
	s.bus.publish(Event{Time: s.now(), Stage: "DEVICE", Mark: "no", Msg: "new device refused",
		Alarm: &Alarm{Title: "DEVICE REFUSED", Sub: why}})
	s.say("DEVICE", "no", "%s was NOT let in to %s: %s", a.deviceName, maskPhone(a.phone), why)
}

// expireApprovals turns silence into a refusal: she is asleep, so nobody
// approves, and the request dies.
func (s *Server) expireApprovals() {
	for _, a := range s.approvals {
		if a.status == "pending" && s.cfg.Now().After(a.expires) {
			s.refuse(a, "expired", "the old phone did not approve it")
		}
	}
}

// ---- login -----------------------------------------------------------------

// loginStart tells the app what this phone must do to get in.
func (s *Server) loginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone    string `json:"phone"`
		DeviceID string `json:"deviceId"`
		HasKey   bool   `json:"hasKey"`
	}
	if !decode(w, r, &req) {
		return
	}
	phone := normalizePhone(req.Phone)
	if len(phone) != 10 || req.DeviceID == "" {
		fail(w, http.StatusBadRequest, "bad_request", "a ten-digit phone number and a deviceId are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAccount(phone)

	if s.mode == ModeLegacy {
		s.say("LOGIN", "", "%s asks to log in from device %s. The old app sends an OTP", maskPhone(phone), short(req.DeviceID))
		s.sendOTP(phone)
		reply(w, http.StatusOK, map[string]any{"next": "otp", "mode": s.mode})
		return
	}

	d := s.devices[req.DeviceID]
	holder := s.boundDevice(phone)
	switch {
	case d != nil && d.acct == phone && d.status == statusBound && req.HasKey:
		n := s.issueNonce(d.id, PurposeLogin, "")
		s.say("LOGIN", "", "%s from its bound phone. Challenge %s… issued, to be signed by the chip", maskPhone(phone), n[:8])
		reply(w, http.StatusOK, map[string]any{"next": "sign", "nonce": n, "mode": s.mode})
	case holder == nil || holder.id == req.DeviceID:
		// Nobody holds this account yet, or this same phone lost its key.
		c := s.issueNonce(req.DeviceID, "enrol", "")
		s.say("LOGIN", "", "%s has no chip bound yet. Binding device %s", maskPhone(phone), short(req.DeviceID))
		reply(w, http.StatusOK, map[string]any{"next": "enrol", "challenge": base64.StdEncoding.EncodeToString([]byte(c)), "mode": s.mode})
	default:
		s.say("LOGIN", "warn", "%s asks to log in from a NEW device %s. Proving the number first", maskPhone(phone), short(req.DeviceID))
		s.sendOTP(phone)
		reply(w, http.StatusOK, map[string]any{"next": "otp", "mode": s.mode})
	}
}

// loginOTP checks the code. In the old app that is the whole login. With the
// chip it only proves the number, which the thief can do too.
func (s *Server) loginOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone      string `json:"phone"`
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
		OTP        string `json:"otp"`
	}
	if !decode(w, r, &req) {
		return
	}
	phone := normalizePhone(req.Phone)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkOTP(phone, req.OTP) {
		s.say("LOGIN", "no", "wrong or expired OTP for %s", maskPhone(phone))
		fail(w, http.StatusUnauthorized, "otp_invalid", "that code is wrong or has expired")
		return
	}

	if s.mode == ModeLegacy {
		d := &device{id: req.DeviceID, acct: phone, name: req.DeviceName, status: statusLegacy, tier: "none", enrolled: s.cfg.Now()}
		s.devices[d.id] = d
		t := randomHex(24)
		s.tokens[t] = &token{deviceID: d.id, issued: s.cfg.Now(), expires: s.cfg.Now().Add(s.cfg.TokenTTL)}
		s.say("LOGIN", "ok", "OTP correct. %s is now trusted for %s. X-Token %s… issued", d.label(), maskPhone(phone), t[:8])
		reply(w, http.StatusOK, map[string]any{"next": "token", "token": t, "expiresInSeconds": int(s.cfg.TokenTTL.Seconds())})
		return
	}

	s.otpPassed[phone+"|"+req.DeviceID] = s.cfg.Now()
	c := s.issueNonce(req.DeviceID, "enrol", "")
	s.say("LOGIN", "warn", "OTP correct. It proves the number and nothing else: %s is bound to another chip", maskPhone(phone))
	reply(w, http.StatusOK, map[string]any{"next": "enrol", "newDevice": true, "challenge": base64.StdEncoding.EncodeToString([]byte(c))})
}

// smsFeed is the phone's SMS inbox. view=forwarded is what the thief sees.
func (s *Server) smsFeed(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(r.URL.Query().Get("phone"))
	forwardedOnly := r.URL.Query().Get("view") == "forwarded"
	var after int
	fmt.Sscan(r.URL.Query().Get("after"), &after)
	s.mu.Lock()
	out := []map[string]any{}
	for _, m := range s.smsLog {
		if m.to != phone || m.id <= after || (forwardedOnly && !m.forwarded) {
			continue
		}
		out = append(out, map[string]any{"id": m.id, "text": m.text, "at": m.at.Format("15:04")})
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"messages": out})
}

// ---- a new phone asks the old phone ----------------------------------------

func (s *Server) approvalsPending(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("deviceId")
	s.mu.Lock()
	s.expireApprovals()
	out := []map[string]any{}
	for _, a := range s.approvals {
		if a.askDevice == deviceID && a.status == "pending" {
			out = append(out, map[string]any{
				"id": a.id, "deviceName": a.deviceName, "tier": a.tier, "newKeyId": a.keyID, "nonce": a.nonce,
				"requestedAt": a.created.Format("15:04"), "expiresInSeconds": int(a.expires.Sub(s.cfg.Now()).Seconds()),
			})
		}
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"approvals": out})
}

func (s *Server) approvalStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.expireApprovals()
	a, ok := s.approvals[r.PathValue("id")]
	status := ""
	if ok {
		status = a.status
	}
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "approval_unknown", "no such approval request")
		return
	}
	reply(w, http.StatusOK, map[string]any{"status": status})
}

// approvalDecision is the old phone answering. Approving needs a signature
// from its chip. Blocking needs nothing: saying no is always safe.
func (s *Server) approvalDecision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID  string `json:"deviceId"`
		Approve   bool   `json:"approve"`
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireApprovals()
	a, ok := s.approvals[r.PathValue("id")]
	if !ok || a.askDevice != req.DeviceID {
		fail(w, http.StatusNotFound, "approval_unknown", "no such approval request for this device")
		return
	}
	if a.status != "pending" {
		fail(w, http.StatusConflict, "approval_closed", "this request is already "+a.status)
		return
	}
	if !req.Approve {
		s.refuse(a, "blocked", "blocked from the old phone")
		reply(w, http.StatusOK, map[string]any{"status": a.status})
		return
	}

	holder := s.devices[a.askDevice]
	signed := signedRequest{DeviceID: req.DeviceID, Payload: req.Payload, Signature: req.Signature}
	p, err := s.verifySigned(holder, signed, PurposeApprove)
	if err == nil && (p.ApprovalID != a.id || p.NewKeyID != a.keyID) {
		err = errors.New("the signature approves a different device")
	}
	if err == nil {
		_, err = s.takeNonce(p.Nonce, holder.id, PurposeApprove)
	}
	s.publishSignature("DEVICE", PurposeApprove+" "+a.id, holder, signed, err)
	if err != nil {
		s.say("DEVICE", "no", "approval for %s rejected: %v", a.deviceName, err)
		fail(w, http.StatusUnauthorized, "bad_signature", err.Error())
		return
	}
	a.status = "approved"
	if d, ok := s.devices[a.newDevice]; ok {
		d.status = statusBound
	}
	s.say("DEVICE", "ok", "%s signed for %s with a fingerprint. The new phone is bound", holder.label(), a.deviceName)
	reply(w, http.StatusOK, map[string]any{"status": a.status})
}

// recoveryStart is "I lost my phone". The demo stops at the door: it says
// what a real backend would ask for, and tells the old phone.
func (s *Server) recoveryStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone    string `json:"phone"`
		DeviceID string `json:"deviceId"`
	}
	if !decode(w, r, &req) {
		return
	}
	phone := normalizePhone(req.Phone)
	s.mu.Lock()
	s.say("RECOVERY", "warn", "“I lost my phone” for %s from device %s", maskPhone(phone), short(req.DeviceID))
	s.say("RECOVERY", "warn", "an OTP is not enough here: live face check, then low limits for 24 hours")
	s.say("RECOVERY", "", "alert sent to the old phone, to email and by SMS: one tap to block")
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{
		"status": "face_check_required", "lowLimit": s.cfg.ReducedLimit, "waitHours": 24,
	})
}

// ---- stage controls --------------------------------------------------------

func (s *Server) demoGet(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := map[string]any{"mode": s.mode, "forwarding": s.forwarding, "approvalSeconds": int(s.cfg.ApprovalTTL.Seconds())}
	s.mu.Unlock()
	reply(w, http.StatusOK, out)
}

// demoSet switches between the two nights and rewinds the story.
func (s *Server) demoSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode       string `json:"mode"`
		Forwarding *bool  `json:"forwarding"`
		Reset      bool   `json:"reset"`
		// KeepKeys makes a reset undo the night but not the fix: phones whose
		// chip already holds the account stay bound and stay logged in.
		KeepKeys bool `json:"keepKeys"`
		// Clear empties the backend and keeps the mode: no devices, logins,
		// payments or messages. It is housekeeping, not part of the story.
		Clear bool `json:"clear"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Mode != "" && req.Mode != ModeChip && req.Mode != ModeLegacy {
		fail(w, http.StatusBadRequest, "bad_request", "mode must be chip or legacy")
		return
	}
	s.mu.Lock()
	if req.Reset || req.Clear {
		devices, tokens := map[string]*device{}, map[string]*token{}
		if req.Reset && req.KeepKeys && s.mode == ModeChip {
			for id, d := range s.devices {
				if d.status == statusBound && d.pub != nil {
					devices[id] = d
				}
			}
			for t, tok := range s.tokens {
				if _, ok := devices[tok.deviceID]; ok {
					tokens[t] = tok
				}
			}
		}
		s.devices, s.tokens = devices, tokens
		s.nonces = map[string]*nonce{}
		s.txns = map[string]*txn{}
		s.balance = map[string]int64{}
		s.otps = map[string]*otpCode{}
		s.otpPassed = map[string]time.Time{}
		s.approvals = map[string]*approval{}
		s.nonceLog, s.txnLog, s.smsLog, s.approvalLog = nil, nil, nil, nil
		s.blocked, s.txnSeq = 0, 48212
		if req.Reset {
			s.bus.publish(Event{Time: s.now(), Stage: "REWIND", Mark: "ok", Msg: "rewound", Reset: true})
			s.say("REWIND", "ok", "same night, same message, same thief. Everything is as it was at 6:40 PM")
			for _, d := range s.devices {
				s.ensureAccount(d.acct)
				s.say("REWIND", "ok", "one change: %s holds a key in its chip for %s", d.label(), maskPhone(d.acct))
			}
		} else {
			s.bus.publish(Event{Time: s.now(), Stage: "CLEAR", Mark: "ok", Msg: "cleared", Clear: true})
			s.say("CLEAR", "ok", "everything cleared: no devices, no logins, no payments, no messages")
		}
	}
	if req.Mode != "" && req.Mode != s.mode {
		s.mode = req.Mode
		s.tokens = map[string]*token{} // sessions belong to the mode that issued them
	}
	if req.Forwarding != nil {
		s.forwarding = *req.Forwarding
	}
	if req.Reset || req.Clear || req.Mode != "" {
		s.say("MODE", "warn", "%s", modeLabel(s.mode))
	}
	out := map[string]any{"mode": s.mode, "forwarding": s.forwarding}
	s.mu.Unlock()
	reply(w, http.StatusOK, out)
}
