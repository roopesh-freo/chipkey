// Package phone is a simulated handset: a software P-256 key plus a fake
// attestation chain. It speaks the same protocol as the Flutter app, so the
// backend can be tested and rehearsed without a device.
package phone

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"chipkey/internal/testchain"
)

// APIError is a non-2xx answer from the backend.
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    map[string]any
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message) }

type Phone struct {
	Base     string
	HTTP     *http.Client
	DeviceID string
	CA       *testchain.Authority
	// Chip describes what the fake secure hardware attests to.
	Chip testchain.Options
	Now  func() time.Time

	Key   *ecdsa.PrivateKey
	KeyID string
	Token string

	// Account is the phone number this handset logs in with, and Name is
	// what the dashboard calls it. Both are optional.
	Account string
	Name    string
}

func New(base string, ca *testchain.Authority, chip testchain.Options) *Phone {
	id := make([]byte, 8)
	rand.Read(id)
	return &Phone{Base: base, HTTP: http.DefaultClient, DeviceID: "d-" + hex.EncodeToString(id), CA: ca, Chip: chip, Now: time.Now}
}

func (p *Phone) call(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, p.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Token != "" {
		req.Header.Set("X-Token", p.Token)
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &APIError{Status: resp.StatusCode, Body: map[string]any{}}
		json.Unmarshal(raw, &e.Body)
		e.Code, _ = e.Body["error"].(string)
		e.Message, _ = e.Body["message"].(string)
		return e
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Challenge asks for an enrolment challenge.
func (p *Phone) Challenge() ([]byte, error) {
	var out struct {
		Challenge string `json:"challenge"`
	}
	if err := p.call("POST", "/v1/enrol/challenge", map[string]string{"deviceId": p.DeviceID}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Challenge)
}

// Attest generates a fresh key and a chain attesting it over challenge.
func (p *Phone) Attest(challenge []byte) ([][]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	chip := p.Chip
	chip.Challenge = challenge
	chain, err := p.CA.Issue(&key.PublicKey, chip)
	if err != nil {
		return nil, err
	}
	p.Key = key
	return chain, nil
}

// Submit posts an attestation chain and returns the backend's verdict.
func (p *Phone) Submit(chain [][]byte) (map[string]any, error) {
	enc := make([]string, len(chain))
	for i, c := range chain {
		enc[i] = base64.StdEncoding.EncodeToString(c)
	}
	var out map[string]any
	body := map[string]any{"deviceId": p.DeviceID, "chain": enc}
	if p.Account != "" {
		body["phone"], body["deviceName"] = p.Account, p.Name
	}
	if err := p.call("POST", "/v1/enrol", body, &out); err != nil {
		return nil, err
	}
	p.KeyID, _ = out["keyId"].(string)
	p.Token = ""
	return out, nil
}

// Enrol runs the whole first act.
func (p *Phone) Enrol() (map[string]any, error) {
	challenge, err := p.Challenge()
	if err != nil {
		return nil, err
	}
	chain, err := p.Attest(challenge)
	if err != nil {
		return nil, err
	}
	return p.Submit(chain)
}

// Sign plays the chip: SHA-256 then ECDSA, DER-encoded, as Android does.
func (p *Phone) Sign(payload []byte) (string, string) {
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, p.Key, digest[:])
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(sig)
}

// LoginPayload builds the bytes signed for a session.
func (p *Phone) LoginPayload(nonce string) []byte {
	b, _ := json.Marshal(map[string]any{
		"purpose": "session.login", "deviceId": p.DeviceID, "keyId": p.KeyID, "nonce": nonce, "ts": p.Now().Unix(),
	})
	return b
}

// PayPayload builds the bytes signed for a payment.
func (p *Phone) PayPayload(txnID, nonce string, amount int64, payee string) []byte {
	b, _ := json.Marshal(map[string]any{
		"purpose": "upi.pay", "txnId": txnID, "amount": amount, "payee": payee,
		"nonce": nonce, "deviceId": p.DeviceID, "keyId": p.KeyID, "ts": p.Now().Unix(),
	})
	return b
}

func (p *Phone) SessionNonce() (string, error) {
	var out struct {
		Nonce string `json:"nonce"`
	}
	err := p.call("POST", "/v1/session/challenge", map[string]string{"deviceId": p.DeviceID}, &out)
	return out.Nonce, err
}

// SubmitLogin posts an already signed login payload.
func (p *Phone) SubmitLogin(payload, signature string) error {
	var out struct {
		Token string `json:"token"`
	}
	if err := p.call("POST", "/v1/session", map[string]string{"deviceId": p.DeviceID, "payload": payload, "signature": signature}, &out); err != nil {
		return err
	}
	p.Token = out.Token
	return nil
}

// Login runs the second act.
func (p *Phone) Login() error {
	nonce, err := p.SessionNonce()
	if err != nil {
		return err
	}
	payload, sig := p.Sign(p.LoginPayload(nonce))
	return p.SubmitLogin(payload, sig)
}

func (p *Phone) Balance() (int64, error) {
	var out struct {
		Balance int64 `json:"balance"`
	}
	err := p.call("GET", "/v1/balance", nil, &out)
	return out.Balance, err
}

// StepUp is the backend asking for a signed transaction.
type StepUp struct {
	TxnID string
	Nonce string
}

// Start opens a payment. Nothing is paid until Confirm. The StepUp is nil
// when the backend is content with the session token.
func (p *Phone) Start(amount int64, payee string) (string, *StepUp, error) {
	var out struct {
		TxnID  string `json:"txnId"`
		StepUp bool   `json:"stepUp"`
		Nonce  string `json:"nonce"`
	}
	if err := p.call("POST", "/v1/pay", map[string]any{"amount": amount, "payee": payee}, &out); err != nil {
		return "", nil, err
	}
	if !out.StepUp {
		return out.TxnID, nil, nil
	}
	return out.TxnID, &StepUp{TxnID: out.TxnID, Nonce: out.Nonce}, nil
}

// Pay starts a payment and, when no step-up is asked for, confirms it at
// once. It returns a StepUp when the backend wants a signed transaction.
func (p *Phone) Pay(amount int64, payee string) (*StepUp, error) {
	txnID, su, err := p.Start(amount, payee)
	if err != nil || su != nil {
		return su, err
	}
	return nil, p.Confirm(txnID, "", "")
}

// Confirm posts a signed transaction payload.
func (p *Phone) Confirm(txnID, payload, signature string) error {
	return p.call("POST", "/v1/pay/confirm", map[string]string{"txnId": txnID, "payload": payload, "signature": signature}, nil)
}

// ---- the two nights: login by phone number ---------------------------------

// Step is the backend telling the phone what to do next.
type Step struct {
	Next      string `json:"next"` // "otp", "enrol", "sign" or "token"
	Nonce     string `json:"nonce"`
	Challenge string `json:"challenge"`
	Token     string `json:"token"`
	NewDevice bool   `json:"newDevice"`
}

// LoginStart opens a login for the phone's account.
func (p *Phone) LoginStart() (*Step, error) {
	var st Step
	err := p.call("POST", "/v1/login/start", map[string]any{"phone": p.Account, "deviceId": p.DeviceID, "hasKey": p.Key != nil}, &st)
	return &st, err
}

// LoginOTP submits a code. In the old app that is the whole login.
func (p *Phone) LoginOTP(code string) (*Step, error) {
	var st Step
	err := p.call("POST", "/v1/login/otp", map[string]any{"phone": p.Account, "deviceId": p.DeviceID, "deviceName": p.Name, "otp": code}, &st)
	if err == nil && st.Token != "" {
		p.Token = st.Token
	}
	return &st, err
}

// Bind makes a key and enrols it against a challenge from a login step.
func (p *Phone) Bind(st *Step) (map[string]any, error) {
	challenge, err := base64.StdEncoding.DecodeString(st.Challenge)
	if err != nil {
		return nil, err
	}
	chain, err := p.Attest(challenge)
	if err != nil {
		return nil, err
	}
	return p.Submit(chain)
}

// SignIn signs a login nonce with the chip and takes the session token.
func (p *Phone) SignIn(nonce string) error {
	payload, sig := p.Sign(p.LoginPayload(nonce))
	return p.SubmitLogin(payload, sig)
}

// OTP reads the newest one-time code sent to a number. forwarded=true reads
// the copy that the malware on her phone passed on.
func (p *Phone) OTP(number string, forwarded bool) (string, error) {
	var out struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	path := "/v1/sms?phone=" + number
	if forwarded {
		path += "&view=forwarded"
	}
	if err := p.call("GET", path, nil, &out); err != nil {
		return "", err
	}
	if len(out.Messages) == 0 {
		return "", fmt.Errorf("no SMS for %s", number)
	}
	return out.Messages[len(out.Messages)-1].Text[:6], nil
}

// Request is a new phone waiting for this phone's approval.
type Request struct {
	ID         string `json:"id"`
	DeviceName string `json:"deviceName"`
	NewKeyID   string `json:"newKeyId"`
	Nonce      string `json:"nonce"`
}

// Pending lists the approval requests addressed to this phone.
func (p *Phone) Pending() ([]Request, error) {
	var out struct {
		Approvals []Request `json:"approvals"`
	}
	err := p.call("GET", "/v1/approvals/pending?deviceId="+p.DeviceID, nil, &out)
	return out.Approvals, err
}

// Decide approves a new phone with a signature from this chip, or blocks it.
func (p *Phone) Decide(r Request, approve bool) error {
	body := map[string]any{"deviceId": p.DeviceID, "approve": approve}
	if approve {
		raw, _ := json.Marshal(map[string]any{
			"purpose": "device.approve", "approvalId": r.ID, "newKeyId": r.NewKeyID,
			"nonce": r.Nonce, "deviceId": p.DeviceID, "keyId": p.KeyID, "ts": p.Now().Unix(),
		})
		body["payload"], body["signature"] = p.Sign(raw)
	}
	return p.call("POST", "/v1/approvals/"+r.ID+"/decision", body, nil)
}

// ApprovalStatus is what the waiting phone polls.
func (p *Phone) ApprovalStatus(id string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	err := p.call("GET", "/v1/approvals/"+id, nil, &out)
	return out.Status, err
}

// Post sends a JSON body to any path. Tests use it for the stage controls.
func (p *Phone) Post(path string, body any, out any) error { return p.call("POST", path, body, out) }

// Get fetches any path.
func (p *Phone) Get(path string, out any) error { return p.call("GET", path, nil, out) }
