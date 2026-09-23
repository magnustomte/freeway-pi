package web

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"freewaypi/internal/i18n"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// handleBackup streams everything needed to rebuild this box onto a fresh card.
//
// Two files: the configuration and the measurement history. Everything else is
// either the binary, which comes from a release, or the unit's own settings,
// which live in the unit and were never ours to keep.
//
// The database is copied through SQLite rather than read off disk, because a
// file copied while it is being written to is a file that may not open.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	stamp := time.Now().Format("2006-01-02-1504")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=freeway-pi-%s.tar.gz", stamp))

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := s.addFile(tw, s.configPath, "config.json"); err != nil {
		// Too late for a status code: the body has begun. A short archive is
		// at least an obviously broken one.
		s.log.Error("backup failed", "err", err)
		return
	}

	if s.history != nil {
		tmp := filepath.Join(os.TempDir(), "freeway-backup-"+stamp+".db")
		if err := s.history.Snapshot(tmp); err != nil {
			s.log.Error("backup: could not copy the history", "err", err)
			return
		}
		defer os.Remove(tmp)
		if err := s.addFile(tw, tmp, "history.db"); err != nil {
			s.log.Error("backup: could not add the history", "err", err)
			return
		}
	}

	guide := restoreGuide(langOf(r))
	if err := s.addBytes(tw, guide.name, []byte(guide.text)); err != nil {
		s.log.Error("backup: could not add the instructions", "err", err)
		return
	}
	s.log.Info("backup downloaded", "from", r.RemoteAddr)
}

func (s *Server) addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func (s *Server) addBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// restoreGuide travels with the backup, because a backup nobody knows how to
// restore is not a backup. It is written for somebody standing at a fresh card
// with no memory of how any of this was set up, in the language they were
// reading the page in when they downloaded it.
//
// Here rather than in the message catalogue: it is a document, not a message,
// and the catalogue's one-line entries are the wrong shape for it.
func restoreGuide(l i18n.Lang) struct{ name, text string } {
	if l == i18n.EN {
		return struct{ name, text string }{"RESTORE.txt", restoreEN}
	}
	return struct{ name, text string }{"GJENOPPRETTING.txt", restoreNO}
}

const restoreEN = `Restoring Freeway Pi
====================

This archive holds everything that belongs to Freeway Pi:

  config.json   Freeway Pi's settings, the PIN verifier and the Modbus
                access list
  history.db    the measurement history

The unit's own settings — setpoint, timer programmes, service interval — live
in the unit and are not in here. They survive this box being replaced.

To set it up again:

  1. Flash the Freeway Pi image to a memory card: the 64-bit one for a Pi 3 or
     later, the 32-bit one for a Pi 2.
  2. Before taking the card out, write freeway.txt on its boot partition with
     your public key and a thirty-minute ssh opening, and the wifi if the box
     will not be on a cable:
         ssh-key=<the whole line from your id_ed25519.pub>
         ssh=30
  3. Move the USB RS-485 adapter to the new box and start it.
  4. Put the two files back:
         scp config.json history.db fwadmin@freeway.local:/tmp/
         ssh fwadmin@freeway.local
         sudo systemctl stop freewayd
         sudo install -o freeway -g freeway -m 640 /tmp/config.json /etc/freeway/config.json
         sudo install -o freeway -g freeway -m 644 /tmp/history.db  /var/lib/freeway/history.db
         sudo systemctl start freewayd
  5. If anything talks to the unit over Modbus TCP by address rather than by
     freeway.local, point it at the new address.

Step 4 can be skipped if you would rather start afresh: the history is lost and
a new PIN has to be chosen, but the unit runs as before throughout.
`

const restoreNO = `Gjenoppretting av Freeway Pi
============================

Denne pakken inneholder alt som hører Freeway Pi til:

  config.json   innstillingene til Freeway Pi, PIN-verifikatoren og
                tilgangslista for Modbus
  history.db    måledataene

Aggregatets egne innstillinger — settpunkt, tidsprogram, serviceintervall —
ligger i aggregatet og er ikke med her. De overlever at denne boksen byttes ut.

Slik setter du det opp på nytt:

  1. Flash Freeway Pi-imaget til et minnekort: 64-bit for Pi 3 og nyere,
     32-bit for Pi 2.
  2. Før du tar ut kortet, skriv freeway.txt på boot-partisjonen med den
     offentlige nøkkelen din og en halvtimes ssh-åpning, og wifi hvis boksen
     ikke skal stå på kabel:
         ssh-key=<hele linja fra id_ed25519.pub>
         ssh=30
  3. Flytt USB-adapteren for RS-485 til den nye boksen og start den.
  4. Legg de to filene tilbake:
         scp config.json history.db fwadmin@freeway.local:/tmp/
         ssh fwadmin@freeway.local
         sudo systemctl stop freewayd
         sudo install -o freeway -g freeway -m 640 /tmp/config.json /etc/freeway/config.json
         sudo install -o freeway -g freeway -m 644 /tmp/history.db  /var/lib/freeway/history.db
         sudo systemctl start freewayd
  5. Hvis noe snakker med aggregatet over Modbus TCP på adresse i stedet for
     freeway.local, pek det på den nye adressen.

Punkt 4 kan hoppes over hvis du heller vil begynne på nytt: måledataene er
borte og en ny PIN må velges, men aggregatet går som før hele veien.
`
