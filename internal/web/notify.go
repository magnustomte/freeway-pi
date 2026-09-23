package web

import (
	"freewaypi/internal/notify"
	"net/http"

	"freewaypi/internal/i18n"

	"freewaypi/internal/config"
)

// notifyConfig is the notification settings as the interface sees them.
//
// The SMTP password is never sent out. It goes in and is stored; what comes
// back says only whether one is set, so an interface that shows the settings
// cannot also leak them to anyone who gets a session.
type notifyConfig struct {
	MinSeverity string           `json:"min_severity"`
	DownAfter   string           `json:"down_after"`
	Webhooks    []config.Webhook `json:"webhooks"`
	SMTP        smtpConfig       `json:"smtp"`
	// Language is what notifications are written in, so the page can say so.
	// Read only: it is set by saving the form in a language.
	Language string `json:"language,omitempty"`
}

type smtpConfig struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	HasPassword bool     `json:"has_password"`
	Password    string   `json:"password,omitempty"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	STARTTLS    bool     `json:"starttls"`
}

func (s *Server) handleNotifyGet(w http.ResponseWriter, r *http.Request) {
	c := s.notifyConfig
	out := notifyConfig{
		MinSeverity: c.MinSeverity,
		DownAfter:   c.DownAfter.Std().String(),
		Webhooks:    c.Webhooks,
		SMTP: smtpConfig{
			Enabled: c.SMTP.Enabled, Host: c.SMTP.Host, Port: c.SMTP.Port,
			Username: c.SMTP.Username, HasPassword: c.SMTP.Password != "",
			From: c.SMTP.From, To: c.SMTP.To, STARTTLS: c.SMTP.STARTTLS,
		},
		Language: string(i18n.Parse(c.Language, "")),
	}
	if out.Webhooks == nil {
		out.Webhooks = []config.Webhook{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	var body notifyConfig
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	for i, h := range body.Webhooks {
		if h.Enabled && h.URL == "" {
			writeError(w, r, http.StatusBadRequest, i18n.Errf("error.webhook.url", i+1))
			return
		}
	}
	if body.SMTP.Enabled {
		if body.SMTP.Host == "" || len(body.SMTP.To) == 0 || body.SMTP.From == "" {
			writeError(w, r, http.StatusBadRequest,
				i18n.Errf("error.smtp.incomplete"))
			return
		}
	}

	err := s.saveConfig(func(c *config.Config) {
		// Whoever sets the notifications up chooses their language, by the
		// language they are reading the page in. A message sent by webhook or
		// mail has no reader with a browser to ask.
		c.Notify.Language = string(langOf(r))
		c.Notify.MinSeverity = body.MinSeverity
		c.Notify.Webhooks = body.Webhooks
		c.Notify.SMTP.Enabled = body.SMTP.Enabled
		c.Notify.SMTP.Host = body.SMTP.Host
		c.Notify.SMTP.Port = body.SMTP.Port
		c.Notify.SMTP.Username = body.SMTP.Username
		c.Notify.SMTP.From = body.SMTP.From
		c.Notify.SMTP.To = body.SMTP.To
		c.Notify.SMTP.STARTTLS = body.SMTP.STARTTLS
		// An empty password leaves the stored one alone, so saving the form
		// without retyping it does not wipe it.
		if body.SMTP.Password != "" {
			c.Notify.SMTP.Password = body.SMTP.Password
		}
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// And take effect now. Saving used to write the file and change nothing
	// until somebody restarted the service — so "send a test" tested a
	// notifier with no channels, which looks exactly like a webhook that does
	// not work.
	current, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.notifier.SetChannels(notify.ChannelsFor(current.Notify),
		notify.ParseSeverity(current.Notify.MinSeverity))
	s.notifier.SetLanguage(langOf(r))
	s.notifyConfig = current.Notify

	s.log.Info("notification settings changed", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"note":     t(r, "note.saved"),
		"language": current.Notify.Language,
	})
}
