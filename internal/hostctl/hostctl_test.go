package hostctl

import "testing"

// TestParseAptStatus covers the three states that look alike in systemd's
// output and mean quite different things to somebody watching a page.
func TestParseAptStatus(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want Apt
	}{{
		// Before anything has ever run, the unit is unknown. systemd answers
		// that with "inactive" and an exit status of 0 — indistinguishable from
		// a job that finished cleanly, except that there is no log.
		name: "nothing has ever run",
		out:  "task=\nstate=inactive\ncode=0\n---\n",
		want: Apt{},
	}, {
		name: "running",
		out:  "task=upgrade\nstate=active\ncode=0\n---\n--- 2026-09-19T10:00:00+02:00 apt-get update\nHit:1 …",
		want: Apt{
			Task: "upgrade", Running: true,
			Log: "--- 2026-09-19T10:00:00+02:00 apt-get update\nHit:1 …",
		},
	}, {
		// systemd reports "activating" for the first moment of a unit's life. A
		// job that showed as stopped for its first second would flicker.
		name: "still starting counts as running",
		out:  "task=refresh\nstate=activating\ncode=0\n---\nstarter…",
		want: Apt{Task: "refresh", Running: true, Log: "starter…"},
	}, {
		name: "finished cleanly",
		out:  "task=refresh\nstate=inactive\ncode=0\n---\nReading package lists...\n--- ferdig",
		want: Apt{
			Task: "refresh", Finished: true,
			Log: "Reading package lists...\n--- ferdig",
		},
	}, {
		name: "failed",
		out:  "task=upgrade\nstate=failed\ncode=100\n---\nE: Could not get lock",
		want: Apt{
			Task: "upgrade", Finished: true, Failed: true, ExitCode: 100,
			Log: "E: Could not get lock",
		},
	}, {
		// A non-zero status with the unit already reaped: systemd says
		// inactive, and only the code says it went wrong.
		name: "exit code alone marks a failure",
		out:  "task=upgrade\nstate=inactive\ncode=1\n---\nnoe gikk galt",
		want: Apt{
			Task: "upgrade", Finished: true, Failed: true, ExitCode: 1,
			Log: "noe gikk galt",
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := *parseAptStatus(c.out)
			if got != c.want {
				t.Errorf("parseAptStatus\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestStartAptRefusesUnknownTasks(t *testing.T) {
	// Checked here as well as in the helper: a typo should not reach a script
	// running as root before anybody notices it is a typo.
	if err := StartApt(nil, "rm -rf"); err == nil {
		t.Fatal("an unknown task was accepted")
	}
}
