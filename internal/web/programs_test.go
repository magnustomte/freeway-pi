package web

import (
	"net/http"
	"testing"
	"time"
)

// The programme this posts is valid; only the clock is in question.
const weekProgramBody = `{"program":{"index":0,"days":62,"start_hour":22,"start_minute":0,` +
	`"stop_hour":7,"stop_minute":30,"function":5}}`

func TestTimerProgramsAreRefusedWhileTheClockIsWrong(t *testing.T) {
	// Timer programs fire by the unit's clock, not ours. Editing a schedule on
	// a unit two hours out produces something that runs at the wrong time, and
	// nothing about the result looks wrong until the house is cold at the
	// wrong hour.
	s, _, ctrl := newServerWithClock(t, 2*time.Hour)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	res := do(t, s, "PUT", "/api/programs/week", weekProgramBody, cookie)
	if res.Code != http.StatusConflict {
		t.Fatalf("returned %d with a two hour drift, want 409", res.Code)
	}
	if len(ctrl.applied) != 0 {
		t.Fatal("the program was written anyway")
	}
	if !contains(res.Body.String(), "klokka") {
		t.Errorf("the refusal does not say what to do about it: %s", res.Body)
	}
}

func TestTimerProgramsGoThroughWhenTheClockIsRight(t *testing.T) {
	s, _, ctrl := newServerWithClock(t, 10*time.Second)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	if res := do(t, s, "PUT", "/api/programs/week", weekProgramBody, cookie); res.Code != http.StatusOK {
		t.Fatalf("returned %d with an accurate clock: %s", res.Code, res.Body)
	}
	if len(ctrl.applied) != 1 {
		t.Fatalf("%d writes applied, want 1", len(ctrl.applied))
	}
}

func TestTheClockGuardCanBeOverriddenDeliberately(t *testing.T) {
	// Deliberately, not by clicking through: the interface offers to set the
	// clock as the easy answer and this as the considered one.
	s, _, ctrl := newServerWithClock(t, 2*time.Hour)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	body := `{"acknowledge_clock_drift":true,"program":{"index":0,"days":62,"start_hour":22,` +
		`"start_minute":0,"stop_hour":7,"stop_minute":30,"function":5}}`
	if res := do(t, s, "PUT", "/api/programs/week", body, cookie); res.Code != http.StatusOK {
		t.Fatalf("an acknowledged edit returned %d: %s", res.Code, res.Body)
	}
	if len(ctrl.applied) != 1 {
		t.Fatalf("%d writes applied, want 1", len(ctrl.applied))
	}
}

func TestTimerProgramsAreBehindThePIN(t *testing.T) {
	s, _, _ := newServerWithClock(t, 0)
	setUpPIN(t, s, "2468")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/programs", ""},
		{"PUT", "/api/programs/week", weekProgramBody},
		{"PUT", "/api/programs/year", `{"program":{"index":0}}`},
	} {
		if res := do(t, s, c.method, c.path, c.body); res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session, want 401", c.method, c.path, res.Code)
		}
	}
}

func TestAnInvalidProgramIsRefusedBeforeTheClockIsEvenConsidered(t *testing.T) {
	s, _, ctrl := newServerWithClock(t, 0)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	bad := `{"program":{"index":0,"days":62,"start_hour":25,"stop_hour":7,"function":5}}`
	if res := do(t, s, "PUT", "/api/programs/week", bad, cookie); res.Code != http.StatusBadRequest {
		t.Fatalf("hour 25 returned %d, want 400", res.Code)
	}
	if len(ctrl.applied) != 0 {
		t.Fatal("an impossible program was written")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRemovingAProgramWorksEvenWhileTheClockIsWrong(t *testing.T) {
	// The guard exists because a schedule set against a wrong clock runs at the
	// wrong time. Removing one cannot do anything at the wrong time, and
	// refusing it would leave somebody unable to undo the very thing the guard
	// is worried about.
	s, _, ctrl := newServerWithClock(t, 2*time.Hour)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	if res := do(t, s, "DELETE", "/api/programs/week?index=0", "", cookie); res.Code != http.StatusOK {
		t.Fatalf("removing a weekly program returned %d: %s", res.Code, res.Body)
	}
	if len(ctrl.applied) != 1 {
		t.Fatalf("%d writes applied, want 1", len(ctrl.applied))
	}
	for i, v := range ctrl.applied[0][0].Regs {
		if v != 0 {
			t.Fatalf("register %d of the cleared program = %d, want 0", i, v)
		}
	}
}

func TestRemovingIsStillBehindThePIN(t *testing.T) {
	s, _, _ := newServerWithClock(t, 0)
	setUpPIN(t, s, "2468")
	if res := do(t, s, "DELETE", "/api/programs/week?index=0", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("returned %d without a session, want 401", res.Code)
	}
}

func TestRemovingNeedsToSayWhich(t *testing.T) {
	s, _, _ := newServerWithClock(t, 0)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")
	for _, q := range []string{"", "?index=", "?index=abc", "?index=99"} {
		if res := do(t, s, "DELETE", "/api/programs/week"+q, "", cookie); res.Code != http.StatusBadRequest {
			t.Errorf("%q returned %d, want 400", q, res.Code)
		}
	}
}

// TestAWeekdayOutOfStepDoesNotBlockEditing records a guard that was built and
// removed. The unit's weekday register reads two days behind its own date, so
// a guard was added on the assumption that weekly programmes are matched
// against it. Measurement said otherwise: a programme set for Friday only
// fired on a Friday while the register said Wednesday. The unit schedules by
// its date, the register is a broken mirror, and guarding on it would have
// refused edits that work.
func TestAWeekdayOutOfStepDoesNotBlockEditing(t *testing.T) {
	s, _, ctrl := newServerWithClock(t, 0)
	ctrl.weekdayWrong = true
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	if res := do(t, s, "PUT", "/api/programs/week", weekProgramBody, cookie); res.Code != http.StatusOK {
		t.Fatalf("returned %d with the weekday register out of step: %s", res.Code, res.Body)
	}
	if len(ctrl.applied) != 1 {
		t.Fatal("the programme did not reach the unit")
	}
}

func TestTheSMTPPasswordIsNeverSentOut(t *testing.T) {
	// An interface that shows the settings must not also hand the password to
	// anyone who gets a session.
	s, _, _ := newServerWithClock(t, 0)
	s.notifyConfig.SMTP.Password = "hemmelig"
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	res := do(t, s, "GET", "/api/notify", "", cookie)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", res.Code, res.Body)
	}
	if contains(res.Body.String(), "hemmelig") {
		t.Fatal("the stored password was sent to the browser")
	}
	if !contains(res.Body.String(), `"has_password":true`) {
		t.Errorf("the interface cannot tell that a password is set: %s", res.Body)
	}
}
