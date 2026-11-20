package api_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"chipkey/api"
	"chipkey/internal/phone"
)

const meerasNumber = "+91 98765 43210"

func (w *world) handset(level int, name string) *phone.Phone {
	p := w.phone(chip(level))
	p.Account, p.Name = meerasNumber, name
	return p
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantNext(t *testing.T, st *phone.Step, err error, next string) {
	t.Helper()
	if err != nil || st.Next != next {
		t.Fatalf("next = %q, %v; want %q", st.Next, err, next)
	}
}

// bindFirst logs Meera's phone in on a fresh account: bind, then sign in.
func bindFirst(t *testing.T, p *phone.Phone) {
	t.Helper()
	st, err := p.LoginStart()
	wantNext(t, st, err, "enrol")
	verdict, err := p.Bind(st)
	if err != nil || verdict["status"] != "bound" {
		t.Fatalf("first phone not bound: %v %v", verdict, err)
	}
	st, err = p.LoginStart()
	wantNext(t, st, err, "sign")
	must(t, p.SignIn(st.Nonce))
}

// newPhoneAsFarAsItGets takes a second phone through OTP and binding, and
// returns the approval it is left waiting on.
func newPhoneAsFarAsItGets(t *testing.T, p *phone.Phone) string {
	t.Helper()
	st, err := p.LoginStart()
	wantNext(t, st, err, "otp")
	code, err := p.OTP("9876543210", true) // the forwarded copy
	must(t, err)
	st, err = p.LoginOTP(code)
	wantNext(t, st, err, "enrol")
	if !st.NewDevice {
		t.Fatal("backend did not flag this as a new device")
	}
	verdict, err := p.Bind(st)
	if err != nil || verdict["status"] != "approval_required" {
		t.Fatalf("new phone verdict = %v %v", verdict, err)
	}
	return verdict["approvalId"].(string)
}

// 11:52 PM, 11:54 PM, 2:07 AM: the night as it happened.
func TestTheNightAsItHappened(t *testing.T) {
	w := start(t, func(c *api.Config) { c.Mode = api.ModeLegacy })
	meera := w.handset(1, "Meera's phone")
	thief := w.handset(2, "The thief's phone")

	// Meera logs in the way she always has: her number and an OTP.
	st, err := meera.LoginStart()
	wantNext(t, st, err, "otp")
	code, err := meera.OTP("9876543210", false)
	must(t, err)
	st, err = meera.LoginOTP(code)
	wantNext(t, st, err, "token")

	// He types her number into his own phone. The OTP goes to her SIM, and
	// the app she installed passes it on.
	st, err = thief.LoginStart()
	wantNext(t, st, err, "otp")
	if _, err := thief.LoginOTP("000000"); err == nil {
		t.Fatal("a guessed OTP was accepted")
	}
	code, err = thief.OTP("9876543210", true)
	must(t, err)
	st, err = thief.LoginOTP(code)
	wantNext(t, st, err, "token")

	// 2:07 AM. No chip, no step-up, no tier: the token is enough.
	for range 3 {
		su, err := thief.Pay(25000, "evil@upi")
		if err != nil || su != nil {
			t.Fatalf("the old app should have paid: %v %v", su, err)
		}
	}
	if bal, _ := meera.Balance(); bal != 25000 {
		t.Fatalf("Meera's balance = %d, want 25000 left of 100000", bal)
	}
	if out := w.log.String(); !strings.Contains(out, "now trusted") || strings.Contains(out, "REFUSED") {
		t.Errorf("the old backend should have seen nothing wrong:\n%s", out)
	}
}

// The same night, with the chip.
func TestTheSameNightWithTheChip(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	thief := w.handset(2, "The thief's phone") // a genuine phone with a better chip
	bindFirst(t, meera)

	id := newPhoneAsFarAsItGets(t, thief)

	// The OTP opened nothing: no session, no balance, no payment.
	_, err := thief.SessionNonce()
	wantCode(t, err, "not_enrolled")
	_, err = thief.Balance()
	wantCode(t, err, "token_invalid")
	st, err := thief.LoginStart()
	wantNext(t, st, err, "otp") // still treated as a stranger

	// Her phone is asked.
	pending, err := meera.Pending()
	if err != nil || len(pending) != 1 || pending[0].DeviceName != "The thief's phone" {
		t.Fatalf("pending on her phone = %+v %v", pending, err)
	}
	if status, _ := thief.ApprovalStatus(id); status != "pending" {
		t.Fatalf("status = %q", status)
	}

	// She is asleep. Nobody approves.
	w.now = w.now.Add(30 * time.Second)
	if status, _ := thief.ApprovalStatus(id); status != "expired" {
		t.Fatalf("status after the window = %q, want expired", status)
	}
	_, err = thief.SessionNonce()
	wantCode(t, err, "not_enrolled")
	if bal, _ := meera.Balance(); bal != 100000 {
		t.Fatalf("Meera's balance = %d", bal)
	}

	// The dashboard raises its alarm exactly once.
	resp, err := http.Get(w.url + "/v1/dash/events")
	must(t, err)
	defer resp.Body.Close()
	alarms := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e api.Event
		if line := sc.Text(); strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(line[6:]), &e) == nil {
			if e.Alarm != nil && e.Alarm.Title == "DEVICE REFUSED" {
				alarms++
			}
			if strings.Contains(e.Msg, "NOT let in") {
				break
			}
		}
	}
	if alarms != 1 {
		t.Errorf("DEVICE REFUSED alarms = %d, want 1", alarms)
	}
}

// Her own upgrade: the old phone signs for the new one.
func TestOldPhoneApprovesANewPhone(t *testing.T) {
	w := start(t, nil)
	old := w.handset(1, "Meera's phone")
	upgrade := w.handset(2, "Meera's new phone")
	bindFirst(t, old)
	id := newPhoneAsFarAsItGets(t, upgrade)
	pending, _ := old.Pending()

	// A signature from the wrong chip does not count, even sent in her name.
	forger := w.handset(2, "forger")
	forger.Attest([]byte("x"))
	forger.DeviceID, forger.KeyID = old.DeviceID, old.KeyID
	wantCode(t, forger.Decide(pending[0], true), "bad_signature")
	if status, _ := upgrade.ApprovalStatus(id); status != "pending" {
		t.Fatalf("a forged approval changed the status to %q", status)
	}

	must(t, old.Decide(pending[0], true))
	if status, _ := upgrade.ApprovalStatus(id); status != "approved" {
		t.Fatalf("status = %q", status)
	}
	st, err := upgrade.LoginStart()
	wantNext(t, st, err, "sign")
	must(t, upgrade.SignIn(st.Nonce))
	if bal, err := upgrade.Balance(); err != nil || bal != 100000 {
		t.Fatalf("approved phone cannot use the account: %d %v", bal, err)
	}
	wantCode(t, old.Decide(pending[0], true), "approval_closed")
}

func TestOldPhoneBlocksANewPhone(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	thief := w.handset(2, "The thief's phone")
	bindFirst(t, meera)
	id := newPhoneAsFarAsItGets(t, thief)
	pending, _ := meera.Pending()

	must(t, meera.Decide(pending[0], false))
	if status, _ := thief.ApprovalStatus(id); status != "blocked" {
		t.Fatalf("status = %q", status)
	}
	if left, _ := meera.Pending(); len(left) != 0 {
		t.Errorf("a blocked request is still pending: %+v", left)
	}
	_, err := thief.SessionNonce()
	wantCode(t, err, "not_enrolled")
}

func TestANewPhoneCannotSkipTheNumberCheck(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	thief := w.handset(2, "The thief's phone")
	bindFirst(t, meera)

	// He asks for a bare enrolment challenge and never proves the number.
	_, err := thief.Enrol()
	wantCode(t, err, "otp_required")
	if pending, _ := meera.Pending(); len(pending) != 0 {
		t.Errorf("an unproven phone reached her approval screen: %+v", pending)
	}
}

func TestLostPhoneIsNotAnOTP(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	thief := w.handset(2, "The thief's phone")
	bindFirst(t, meera)
	newPhoneAsFarAsItGets(t, thief)

	var out map[string]any
	must(t, thief.Post("/v1/recovery/start", map[string]any{"phone": meerasNumber, "deviceId": thief.DeviceID}, &out))
	if out["status"] != "face_check_required" {
		t.Fatalf("recovery answered %v", out)
	}
	_, err := thief.SessionNonce()
	wantCode(t, err, "not_enrolled")
}

func TestRewindSwitchesNightsAndForgetsEverything(t *testing.T) {
	w := start(t, func(c *api.Config) { c.Mode = api.ModeLegacy })
	thief := w.handset(2, "The thief's phone")
	st, _ := thief.LoginStart()
	code, _ := thief.OTP("9876543210", true)
	st, err := thief.LoginOTP(code)
	wantNext(t, st, err, "token")
	thief.Pay(25000, "evil@upi")

	var demo map[string]any
	must(t, thief.Post("/v1/demo", map[string]any{"reset": true, "mode": "chip"}, &demo))
	if demo["mode"] != "chip" {
		t.Fatalf("demo = %v", demo)
	}
	_, err = thief.Balance()
	wantCode(t, err, "token_invalid")

	var state struct {
		Mode    string           `json:"mode"`
		Devices []map[string]any `json:"devices"`
		Txns    []map[string]any `json:"txns"`
		SMS     []map[string]any `json:"sms"`
	}
	must(t, thief.Get("/v1/dash/state", &state))
	if state.Mode != "chip" || len(state.Devices) != 0 || len(state.Txns) != 0 || len(state.SMS) != 0 {
		t.Fatalf("state after rewind = %+v", state)
	}

	// The same phone that stole last night is a stranger tonight.
	meera := w.handset(1, "Meera's phone")
	bindFirst(t, meera)
	if bal, _ := meera.Balance(); bal != 100000 {
		t.Fatalf("balance after rewind = %d", bal)
	}
	thief.Key, thief.Token = nil, ""
	st, err = thief.LoginStart()
	wantNext(t, st, err, "otp")
	wantCode(t, thief.Post("/v1/demo", map[string]any{"mode": "nonsense"}, nil), "bad_request")
}

func TestRewindCanKeepHerKey(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	bindFirst(t, meera)
	if _, err := meera.Pay(500, "chai@upi"); err != nil {
		t.Fatal(err)
	}

	// The dashboard's R key: undo the night, keep the fix.
	must(t, meera.Post("/v1/demo", map[string]any{"reset": true, "mode": "chip", "keepKeys": true}, nil))
	bal, err := meera.Balance()
	if err != nil || bal != 100000 {
		t.Fatalf("her session should survive and her money come back: balance = %d, err = %v", bal, err)
	}
	var state struct {
		Devices []map[string]any `json:"devices"`
		Txns    []map[string]any `json:"txns"`
	}
	must(t, meera.Get("/v1/dash/state", &state))
	if len(state.Devices) != 1 || len(state.Txns) != 0 {
		t.Fatalf("state after a keeping rewind = %+v", state)
	}

	// The thief is still a stranger, and still needs her phone's word.
	thief := w.handset(2, "The thief's phone")
	st, err := thief.LoginStart()
	wantNext(t, st, err, "otp")

	// A plain reset forgets her key too.
	must(t, meera.Post("/v1/demo", map[string]any{"reset": true}, nil))
	if _, err := meera.Balance(); err == nil {
		t.Fatal("a full reset must end her session")
	}
}

func TestClearEmptiesTheBackendAndKeepsTheMode(t *testing.T) {
	w := start(t, nil)
	meera := w.handset(1, "Meera's phone")
	bindFirst(t, meera)
	if _, err := meera.Pay(500, "chai@upi"); err != nil {
		t.Fatal(err)
	}

	// The dashboard's Clear button.
	var demo struct {
		Mode string `json:"mode"`
	}
	must(t, meera.Post("/v1/demo", map[string]any{"clear": true}, &demo))
	if demo.Mode != "chip" {
		t.Fatalf("clear changed the mode to %q", demo.Mode)
	}
	if _, err := meera.Balance(); err == nil {
		t.Fatal("clear must end her session")
	}
	var state struct {
		Devices []map[string]any `json:"devices"`
		Tokens  []map[string]any `json:"tokens"`
		Txns    []map[string]any `json:"txns"`
		SMS     []map[string]any `json:"sms"`
		Mode    string           `json:"mode"`
	}
	must(t, meera.Get("/v1/dash/state", &state))
	if len(state.Devices)+len(state.Tokens)+len(state.Txns)+len(state.SMS) != 0 || state.Mode != "chip" {
		t.Fatalf("state after clear = %+v", state)
	}

	// The dashboard is told to empty its screen, without the rewind splash.
	resp, err := http.Get(w.url + "/v1/dash/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cleared, rewound := 0, 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e api.Event
		if line := sc.Text(); strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(line[6:]), &e) == nil {
			if e.Clear {
				cleared++
			}
			if e.Reset {
				rewound++
			}
			// The mode line is the last thing a clear writes.
			if cleared > 0 && e.Stage == "MODE" {
				break
			}
		}
	}
	if cleared != 1 || rewound != 0 {
		t.Fatalf("clear events = %d, rewind events = %d; want 1 and 0", cleared, rewound)
	}

	// The same phone starts again from nothing.
	bindFirst(t, meera)
}
