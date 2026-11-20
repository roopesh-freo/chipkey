// Package api is the demo backend: enrol a hardware key, trade a signed
// nonce for a session token, and demand a signed transaction for payments
// above a threshold. State lives in memory. It is a talk demo, not a bank.
package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"chipkey/attest"
)

const (
	PurposeLogin = "session.login"
	PurposePay   = "upi.pay"

	TierStrongBox = "strongbox"
	TierTEE       = "tee"
	TierSoftware  = "software"
)

// Config is the backend's policy.
type Config struct {
	// Package is the Android application ID that may enrol. Empty skips the check.
	Package string
	// CertDigest is the SHA-256 of the app's signing certificate. Nil skips the check.
	CertDigest []byte
	// AllowSoftware enrols phones that cannot prove hardware backing into the
	// lowest tier instead of turning them away.
	AllowSoftware bool
	// Threshold is the amount in rupees at which a payment needs step-up.
	Threshold int64
	// ReducedLimit caps a single payment from a phone without full trust.
	ReducedLimit int64
	TokenTTL     time.Duration
	NonceTTL     time.Duration
	// ClockSkew is how far a signed timestamp may be from the server clock.
	ClockSkew time.Duration
	// ApprovalTTL is how long the old phone has to approve a new one.
	ApprovalTTL time.Duration
	// Mode is ModeChip or ModeLegacy. Legacy is the app as it was before the
	// chip: a phone number is proven by an OTP and nothing else.
	Mode string
	// DashUser and DashPass are the dashboard login. An empty DashPass leaves
	// the dashboard and the stage controls open.
	DashUser string
	DashPass string
	Attest   attest.Options
	Out      io.Writer
	Color    bool
	Now      func() time.Time
}

type device struct {
	id        string
	pub       *ecdsa.PublicKey
	keyID     string
	tier      string
	integrity bool
	// The rest is only for the dashboard.
	root     string
	boot     string
	enrolled time.Time
	// acct is the account this device acts for: a phone number, or a private
	// account for callers that never give one.
	acct string
	name string
	// status is statusBound, statusPending, statusRejected or statusLegacy.
	status string
}

// full reports whether the device earns full limits: a hardware key on a
// phone whose boot chain the chip vouches for.
func (d *device) full() bool { return d.tier != TierSoftware && d.integrity }

type nonce struct {
	value    string
	deviceID string
	purpose  string
	txnID    string
	issued   time.Time
	expires  time.Time
	status   string // "pending", "used" or "rejected"
}

type token struct {
	deviceID string
	issued   time.Time
	expires  time.Time
}

type txn struct {
	id       string
	deviceID string
	amount   int64
	payee    string
	paid     bool
	// stepUp is decided by the backend when the payment starts. A payment
	// that needs the chip can never be confirmed without a signature.
	stepUp bool
	// auth is how the payment was authorised: "token" or "chip".
	auth      string
	created   time.Time
	attempts  int
	lastError string
}

// Server holds the demo state and implements http.Handler.
type Server struct {
	cfg Config
	mux *http.ServeMux

	mu      sync.Mutex
	devices map[string]*device
	nonces  map[string]*nonce
	tokens  map[string]*token
	txns    map[string]*txn
	balance map[string]int64
	txnSeq  int

	// Dashboard state: a live event stream plus history the handlers no
	// longer need but an audience likes to see.
	bus      *bus
	nonceLog []*nonce
	txnLog   []*txn
	blocked  int

	// The two nights: accounts by phone number, SMS that can be forwarded,
	// and approval of a new phone by the old one.
	mode        string
	forwarding  bool
	smsLog      []*sms
	smsSeq      int
	otps        map[string]*otpCode
	otpPassed   map[string]time.Time
	approvals   map[string]*approval
	approvalLog []*approval
}

func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = 15 * time.Minute
	}
	if cfg.NonceTTL == 0 {
		cfg.NonceTTL = time.Minute
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 5 * time.Minute
	}
	if cfg.ApprovalTTL == 0 {
		cfg.ApprovalTTL = 20 * time.Second
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeChip
	}
	s := &Server{
		cfg:     cfg,
		mux:     http.NewServeMux(),
		devices: map[string]*device{},
		nonces:  map[string]*nonce{},
		tokens:  map[string]*token{},
		txns:    map[string]*txn{},
		balance: map[string]int64{},
		txnSeq:  48212,
		bus:     newBus(),

		mode:       cfg.Mode,
		forwarding: true,
		otps:       map[string]*otpCode{},
		otpPassed:  map[string]time.Time{},
		approvals:  map[string]*approval{},
	}
	s.nightRoutes()
	s.mux.HandleFunc("POST /v1/enrol/challenge", s.enrolChallenge)
	s.mux.HandleFunc("POST /v1/enrol", s.enrol)
	s.mux.HandleFunc("POST /v1/session/challenge", s.sessionChallenge)
	s.mux.HandleFunc("POST /v1/session", s.session)
	s.mux.HandleFunc("GET /v1/balance", s.getBalance)
	s.mux.HandleFunc("POST /v1/pay", s.pay)
	s.mux.HandleFunc("POST /v1/pay/confirm", s.payConfirm)
	s.mux.HandleFunc("GET /{$}", s.dashboard)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("GET /v1/dash/events", s.staff(s.dashEvents))
	s.mux.HandleFunc("GET /v1/dash/state", s.staff(s.dashState))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	s.say("BOOT", "ok", "trust store loaded: %d attestation roots", len(cfg.Attest.Roots))
	s.say("BOOT", "ok", "policy: step-up at %s, X-Token valid %s, challenge valid %s", Rupees(cfg.Threshold), cfg.TokenTTL, cfg.NonceTTL)
	s.say("BOOT", "", "mode: %s. Waiting for a device", modeLabel(s.mode))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ---- helpers ---------------------------------------------------------------

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(body, v)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]string{"error": code, "message": message})
}

// Rupees formats an amount with Indian digit grouping: 1,25,000.
func Rupees(n int64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return "₹" + s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var groups []string
	for len(head) > 2 {
		groups = append([]string{head[len(head)-2:]}, groups...)
		head = head[:len(head)-2]
	}
	groups = append([]string{head}, groups...)
	return "₹" + strings.Join(groups, ",") + "," + tail
}

const (
	cReset  = "\033[0m"
	cDim    = "\033[2m"
	cBold   = "\033[1m"
	cRed    = "\033[1;31m"
	cGreen  = "\033[1;32m"
	cYellow = "\033[1;33m"
	cCyan   = "\033[1;36m"
)

// say prints one decision line to the terminal the audience is watching.
// mark is "ok", "no", "warn" or "" for a neutral step.
func (s *Server) say(stage, mark, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sym, colour := "  ", ""
	switch mark {
	case "ok":
		sym, colour = "✔ ", cGreen
	case "no":
		sym, colour = "✘ ", cRed
	case "warn":
		sym, colour = "! ", cYellow
	}
	ts := s.cfg.Now().Format("15:04:05")
	s.bus.publish(Event{Time: ts, Stage: stage, Mark: mark, Msg: msg})
	if s.cfg.Color {
		fmt.Fprintf(s.cfg.Out, "%s%s%s  %s%-8s%s %s%s%s%s\n", cDim, ts, cReset, cCyan, stage, cReset, colour, sym, msg, cReset)
		return
	}
	fmt.Fprintf(s.cfg.Out, "%s  %-8s %s%s\n", ts, stage, sym, msg)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// issueNonce stores a single-use nonce. Callers hold s.mu.
func (s *Server) issueNonce(deviceID, purpose, txnID string) string {
	now := s.cfg.Now()
	for k, n := range s.nonces {
		if now.After(n.expires) {
			delete(s.nonces, k)
		}
	}
	v := randomHex(16)
	n := &nonce{value: v, deviceID: deviceID, purpose: purpose, txnID: txnID, issued: now, expires: now.Add(s.cfg.NonceTTL), status: "pending"}
	s.nonces[v] = n
	if s.nonceLog = append(s.nonceLog, n); len(s.nonceLog) > 40 {
		s.nonceLog = s.nonceLog[len(s.nonceLog)-40:]
	}
	return v
}

// takeNonce consumes a nonce if it is known, unexpired and was issued for
// this device and purpose. Callers hold s.mu.
func (s *Server) takeNonce(value, deviceID, purpose string) (*nonce, error) {
	n, ok := s.nonces[value]
	if !ok {
		return nil, errors.New("nonce is unknown or already used")
	}
	delete(s.nonces, value)
	if s.cfg.Now().After(n.expires) {
		n.status = "rejected"
		return nil, errors.New("nonce has expired")
	}
	if n.deviceID != deviceID || n.purpose != purpose {
		n.status = "rejected"
		return nil, errors.New("nonce was issued for a different device or purpose")
	}
	n.status = "used"
	return n, nil
}

// signedPayload is what the chip signs. The server verifies the signature
// over the exact bytes it received, and only then reads the fields.
type signedPayload struct {
	Purpose  string `json:"purpose"`
	DeviceID string `json:"deviceId"`
	KeyID    string `json:"keyId"`
	Nonce    string `json:"nonce"`
	Ts       int64  `json:"ts"`
	TxnID    string `json:"txnId,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	Payee    string `json:"payee,omitempty"`
	// Set when an old phone approves a new one.
	ApprovalID string `json:"approvalId,omitempty"`
	NewKeyID   string `json:"newKeyId,omitempty"`
}

type signedRequest struct {
	DeviceID  string `json:"deviceId"`
	TxnID     string `json:"txnId"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// verifySigned checks the ECDSA signature over the raw payload bytes with
// the public key stored at enrolment, then parses and sanity-checks it.
func (s *Server) verifySigned(d *device, req signedRequest, purpose string) (*signedPayload, error) {
	raw, err := base64.StdEncoding.DecodeString(req.Payload)
	if err != nil {
		return nil, errors.New("payload is not base64")
	}
	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return nil, errors.New("signature is not base64")
	}
	digest := sha256.Sum256(raw)
	if !ecdsa.VerifyASN1(d.pub, digest[:], sig) {
		return nil, errBadSignature
	}
	var p signedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("signed payload is not valid JSON")
	}
	if p.Purpose != purpose {
		return nil, fmt.Errorf("signed for purpose %q, not %q", p.Purpose, purpose)
	}
	if p.DeviceID != d.id || p.KeyID != d.keyID {
		return nil, errors.New("signed for a different device or key")
	}
	if skew := s.cfg.Now().Sub(time.Unix(p.Ts, 0)).Abs(); skew > s.cfg.ClockSkew {
		return nil, fmt.Errorf("signed timestamp is %s away from the server clock", skew.Round(time.Second))
	}
	return &p, nil
}

var errBadSignature = errors.New("signature does not match the enrolled key")

// auth resolves the X-Token header to a device. Callers hold s.mu.
func (s *Server) auth(r *http.Request) (*device, bool) {
	t, ok := s.tokens[r.Header.Get("X-Token")]
	if !ok || s.cfg.Now().After(t.expires) {
		return nil, false
	}
	d, ok := s.devices[t.deviceID]
	return d, ok
}

// ---- Act 1: enrol ----------------------------------------------------------

func (s *Server) enrolChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.DeviceID == "" {
		fail(w, http.StatusBadRequest, "bad_request", "deviceId is required")
		return
	}
	s.mu.Lock()
	challenge := s.issueNonce(req.DeviceID, "enrol", "")
	s.mu.Unlock()
	s.say("ENROL", "", "challenge issued to device %s", short(req.DeviceID))
	reply(w, http.StatusOK, map[string]any{
		"challenge": base64.StdEncoding.EncodeToString([]byte(challenge)),
	})
}

func (s *Server) enrol(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID   string   `json:"deviceId"`
		Chain      []string `json:"chain"`
		Phone      string   `json:"phone"`
		DeviceName string   `json:"deviceName"`
	}
	if !decode(w, r, &req) {
		return
	}
	chain := make([][]byte, 0, len(req.Chain))
	for _, c := range req.Chain {
		der, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "chain entries must be base64 DER certificates")
			return
		}
		chain = append(chain, der)
	}

	reject := func(code, why string) {
		s.say("ENROL", "no", "rejected: %s", why)
		fail(w, http.StatusForbidden, code, why)
	}

	// 1. Is the chain genuine?
	trusted := true
	res, err := attest.Verify(chain, s.cfg.Attest)
	if errors.Is(err, attest.ErrUntrustedRoot) && s.cfg.AllowSoftware {
		trusted = false
		s.say("ENROL", "warn", "%v", err)
		res, err = attest.DescribeUnverified(chain)
	}
	if err != nil {
		reject("attestation_rejected", err.Error())
		return
	}
	if trusted {
		s.say("ENROL", "ok", "chain of %d certificates ends at %s", res.ChainLen, res.RootName)
	}

	// 2. Is it an answer to our challenge, for this device, used once?
	s.mu.Lock()
	_, nerr := s.takeNonce(string(res.Challenge), req.DeviceID, "enrol")
	s.mu.Unlock()
	if !res.HasAttestation || nerr != nil {
		reject("challenge_mismatch", "attestation does not carry the challenge we issued")
		return
	}
	s.say("ENROL", "ok", "challenge in the certificate equals our nonce")

	// 3. Is it our app, with a key the chip guards behind user authentication?
	if s.cfg.Package != "" && !slices.Contains(res.PackageNames, s.cfg.Package) {
		reject("wrong_app", fmt.Sprintf("key was requested by %v, not %s", res.PackageNames, s.cfg.Package))
		return
	}
	if s.cfg.CertDigest != nil && !slices.ContainsFunc(res.SignatureDigests, func(d []byte) bool { return bytes.Equal(d, s.cfg.CertDigest) }) {
		reject("wrong_signer", "app is not signed with our certificate")
		return
	}
	if trusted && !res.UserAuthRequired {
		reject("key_not_auth_bound", "key can be used without biometrics")
		return
	}

	// 4. Which tier did it earn? Read from the certificate, never from the app.
	tier := TierSoftware
	if trusted && res.AttestationLevel != attest.Software {
		switch res.KeyLevel {
		case attest.StrongBox:
			tier = TierStrongBox
		case attest.TrustedEnvironment:
			tier = TierTEE
		}
	}
	integrity := trusted && res.Integrity()

	keyDigest := sha256.Sum256(res.SPKI)
	d := &device{id: req.DeviceID, pub: res.PublicKey, keyID: hex.EncodeToString(keyDigest[:8]), tier: tier, integrity: integrity, enrolled: s.cfg.Now(),
		acct: "device:" + req.DeviceID, name: req.DeviceName, status: statusBound}
	phone := normalizePhone(req.Phone)

	s.mu.Lock()
	var pending *approval
	var holder *device
	if phone != "" {
		d.acct = phone
		if holder = s.boundDevice(phone); holder != nil && holder.id != d.id {
			// Another chip already holds this account. An OTP got this phone
			// as far as here. It gets no further without the old phone.
			if !s.otpFresh(phone, d.id) {
				s.mu.Unlock()
				reject("otp_required", "prove the phone number before binding a new device")
				return
			}
			d.status = statusPending
			pending = s.openApproval(phone, d, holder)
		}
	}
	_, again := s.devices[d.id]
	s.devices[d.id] = d
	s.ensureAccount(d.acct)
	for k, t := range s.tokens { // a new key ends every old session
		if t.deviceID == d.id {
			delete(s.tokens, k)
		}
	}
	s.mu.Unlock()

	boot := "no root of trust"
	if !trusted {
		boot = "nothing this phone claims is believed"
	} else if res.HasRootOfTrust {
		boot = fmt.Sprintf("boot=%s bootloader locked=%v", res.BootState, res.DeviceLocked)
	}
	mark := "ok"
	if !d.full() {
		mark = "warn"
	}
	s.mu.Lock()
	d.boot, d.root = boot, res.RootName
	if !trusted {
		d.root = "untrusted"
	}
	s.mu.Unlock()
	s.bus.publish(Event{Time: s.cfg.Now().Format("15:04:05"), Stage: "ENROL", Mark: mark,
		Msg: "attestation decoded", Detail: attestationDetail(chain, res, trusted, tier, d)})
	s.say("ENROL", mark, "tier=%s  %s  app=%s", strings.ToUpper(tier), boot, strings.Join(res.PackageNames, ","))
	if len(res.SignatureDigests) > 0 && s.cfg.CertDigest == nil {
		s.say("ENROL", "", "signing certificate digest %x (not pinned)", res.SignatureDigests[0])
	}
	if pending != nil {
		s.say("DEVICE", "warn", "%s wants to join account %s, which is bound to another chip", d.label(), maskPhone(phone))
		s.say("DEVICE", "warn", "asking %s to approve with a fingerprint, valid %s", holder.label(), s.cfg.ApprovalTTL)
		reply(w, http.StatusOK, map[string]any{
			"status": "approval_required", "approvalId": pending.id,
			"keyId": d.keyID, "tier": d.tier, "integrity": d.integrity, "fullLimits": d.full(),
			"reducedLimit": s.cfg.ReducedLimit, "threshold": s.cfg.Threshold,
		})
		return
	}
	limits := "full limits"
	if !d.full() {
		limits = "reduced limits: " + Rupees(s.cfg.ReducedLimit) + " per payment"
	}
	verb := "registered"
	if again {
		verb = "re-enrolled, old key and sessions dropped"
	}
	s.say("ENROL", mark, "public key %s, key %s, %s", verb, d.keyID, limits)

	reply(w, http.StatusOK, map[string]any{
		"status":       "bound",
		"keyId":        d.keyID,
		"tier":         d.tier,
		"integrity":    d.integrity,
		"fullLimits":   d.full(),
		"reducedLimit": s.cfg.ReducedLimit,
		"threshold":    s.cfg.Threshold,
	})
}

// ---- Act 2: session --------------------------------------------------------

func (s *Server) sessionChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	d, known := s.devices[req.DeviceID]
	known = known && d.status == statusBound
	var n string
	if known {
		n = s.issueNonce(req.DeviceID, PurposeLogin, "")
	}
	s.mu.Unlock()
	if !known {
		s.say("SESSION", "no", "device %s is not enrolled", short(req.DeviceID))
		fail(w, http.StatusNotFound, "not_enrolled", "device is not enrolled")
		return
	}
	s.say("SESSION", "", "nonce %s… issued, valid %s, single use", n[:8], s.cfg.NonceTTL)
	reply(w, http.StatusOK, map[string]any{"nonce": n})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	var req signedRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[req.DeviceID]
	if !ok || d.status != statusBound {
		fail(w, http.StatusNotFound, "not_enrolled", "device is not enrolled")
		return
	}
	p, err := s.verifySigned(d, req, PurposeLogin)
	if err == nil {
		_, err = s.takeNonce(p.Nonce, d.id, PurposeLogin)
	}
	s.publishSignature("SESSION", PurposeLogin, d, req, err)
	if err != nil {
		s.say("SESSION", "no", "rejected: %v", err)
		fail(w, http.StatusUnauthorized, "bad_signature", err.Error())
		return
	}
	t := randomHex(24)
	s.tokens[t] = &token{deviceID: d.id, issued: s.cfg.Now(), expires: s.cfg.Now().Add(s.cfg.TokenTTL)}
	s.say("SESSION", "ok", "signature valid for key %s, purpose=%s", d.keyID, p.Purpose)
	s.say("SESSION", "ok", "X-Token %s… issued, valid %s", t[:8], s.cfg.TokenTTL)
	reply(w, http.StatusOK, map[string]any{"token": t, "expiresInSeconds": int(s.cfg.TokenTTL.Seconds())})
}

func (s *Server) getBalance(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.auth(r)
	if !ok {
		s.say("API", "no", "GET /v1/balance without a valid X-Token")
		fail(w, http.StatusUnauthorized, "token_invalid", "X-Token is missing, unknown or expired")
		return
	}
	s.say("API", "ok", "GET /v1/balance on X-Token, balance %s", Rupees(s.balance[d.acct]))
	reply(w, http.StatusOK, map[string]any{"balance": s.balance[d.acct]})
}

// ---- Act 3: step-up --------------------------------------------------------

func (s *Server) pay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Amount int64  `json:"amount"`
		Payee  string `json:"payee"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.auth(r)
	if !ok {
		s.say("PAY", "no", "payment without a valid X-Token")
		fail(w, http.StatusUnauthorized, "token_invalid", "X-Token is missing, unknown or expired")
		return
	}
	if req.Amount <= 0 || req.Payee == "" {
		fail(w, http.StatusBadRequest, "bad_request", "amount and payee are required")
		return
	}
	if req.Amount > s.balance[d.acct] {
		s.say("PAY", "no", "%s to %s exceeds the balance", Rupees(req.Amount), req.Payee)
		fail(w, http.StatusConflict, "insufficient_funds", "balance is too low")
		return
	}
	if s.mode != ModeLegacy && !d.full() && req.Amount > s.cfg.ReducedLimit {
		s.say("PAY", "no", "%s to %s refused: reduced limits, max %s (tier=%s, verified boot=%v)", Rupees(req.Amount), req.Payee, Rupees(s.cfg.ReducedLimit), strings.ToUpper(d.tier), d.integrity)
		fail(w, http.StatusForbidden, "limit_exceeded",
			fmt.Sprintf("this phone cannot prove hardware trust, so payments are limited to %s", Rupees(s.cfg.ReducedLimit)))
		return
	}

	s.txnSeq++
	t := &txn{id: fmt.Sprintf("T-%d", s.txnSeq), deviceID: d.id, amount: req.Amount, payee: req.Payee, created: s.cfg.Now()}
	s.txns[t.id] = t
	if s.txnLog = append(s.txnLog, t); len(s.txnLog) > 40 {
		s.txnLog = s.txnLog[len(s.txnLog)-40:]
	}

	// Nothing is paid here. The app confirms after the user has approved,
	// so a payment never completes behind a screen the user has not seen.
	if s.mode == ModeLegacy {
		s.say("PAY", "", "%s to %s accepted on the session token, as the old app did  %s", Rupees(t.amount), t.payee, t.id)
		reply(w, http.StatusOK, map[string]any{"status": "pending", "txnId": t.id, "stepUp": false})
		return
	}
	if req.Amount < s.cfg.Threshold {
		s.say("PAY", "", "%s to %s is under %s, no step-up needed  %s", Rupees(t.amount), t.payee, Rupees(s.cfg.Threshold), t.id)
		reply(w, http.StatusOK, map[string]any{"status": "pending", "txnId": t.id, "stepUp": false})
		return
	}

	t.stepUp = true
	n := s.issueNonce(d.id, PurposePay, t.id)
	s.say("PAY", "warn", "%s to %s is at or over %s, step-up required  %s", Rupees(t.amount), t.payee, Rupees(s.cfg.Threshold), t.id)
	reply(w, http.StatusOK, map[string]any{"status": "pending", "txnId": t.id, "stepUp": true, "nonce": n})
}

func (s *Server) payConfirm(w http.ResponseWriter, r *http.Request) {
	var req signedRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.auth(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "token_invalid", "X-Token is missing, unknown or expired")
		return
	}
	t, ok := s.txns[req.TxnID]
	if !ok || t.deviceID != d.id || t.paid {
		s.say("PAY", "no", "no pending transaction %s for this device", req.TxnID)
		fail(w, http.StatusNotFound, "txn_unknown", "no pending transaction with that id")
		return
	}

	if !t.stepUp {
		t.paid, t.auth = true, "token"
		s.balance[d.acct] -= t.amount
		s.say("PAY", "ok", "%s to %s confirmed, paid on X-Token alone  %s", Rupees(t.amount), t.payee, t.id)
		reply(w, http.StatusOK, map[string]any{"status": "paid", "txnId": t.id, "balance": s.balance[d.acct]})
		return
	}

	p, err := s.verifySigned(d, req, PurposePay)
	if errors.Is(err, errBadSignature) {
		t.attempts, t.lastError = t.attempts+1, "signature invalid"
		s.blocked++
		s.publishSignature("PAY", PurposePay+" "+t.id, d, req, err)
		s.say("PAY", "no", "signature INVALID for %s: the payload changed after the chip signed it", t.id)
		fail(w, http.StatusUnauthorized, "bad_signature", err.Error())
		return
	}
	if err == nil && (p.TxnID != t.id || p.Amount != t.amount || p.Payee != t.payee) {
		err = fmt.Errorf("user approved %s to %s, but %s is %s to %s", Rupees(p.Amount), p.Payee, t.id, Rupees(t.amount), t.payee)
	}
	if err == nil {
		var n *nonce
		if n, err = s.takeNonce(p.Nonce, d.id, PurposePay); err == nil && n.txnID != t.id {
			err = errors.New("nonce was issued for a different transaction")
		}
	}
	s.publishSignature("PAY", PurposePay+" "+t.id, d, req, err)
	if err != nil {
		t.attempts, t.lastError = t.attempts+1, "payload mismatch"
		s.blocked++
		s.say("PAY", "no", "rejected %s: %v", t.id, err)
		fail(w, http.StatusUnauthorized, "payload_mismatch", err.Error())
		return
	}

	t.paid, t.auth, t.lastError = true, "chip", ""
	s.balance[d.acct] -= t.amount
	s.say("PAY", "ok", "signature valid for key %s", d.keyID)
	s.say("PAY", "ok", "signed payload matches %s: %s to %s. Paid.", t.id, Rupees(t.amount), t.payee)
	reply(w, http.StatusOK, map[string]any{"status": "paid", "txnId": t.id, "balance": s.balance[d.acct]})
}
