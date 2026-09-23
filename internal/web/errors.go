package web

import "freewaypi/internal/i18n"

// errNetworkNotEnabled explains what to do rather than only refusing.
//
// It used to give a sudoers line for a helper that no longer exists — the
// privilege split went to a socket-activated service, and install.sh removes
// the old rule. Telling somebody to grant a sudo right that nothing reads is
// worse than telling them nothing.
func errNetworkNotEnabled(why string) error {
	return i18n.Errf("error.network.notenabled", why)
}

// errNoWiFi is the answer when there is no radio to configure. A box with no
// wifi interface is the normal case — this appliance wants a cable — so this
// says so rather than reporting a failure.
func errNoWiFi() error {
	return i18n.Errf("error.wifi.unsupported")
}
