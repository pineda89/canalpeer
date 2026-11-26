package main

// launcherHandlers are what the launcher page can ask of the app.
type launcherHandlers struct {
	watch    func(text string) (errText string) // "" when the link is accepted
	download func()
	licenses func()
	forget   func(id string) // drops a channel from the history
	setVpn   func(mode string)
}

// launcherWindow is an open launcher: close asks it to go away; closed is
// closed once it is gone, whoever closed it.
type launcherWindow struct {
	close  func()
	closed <-chan struct{}
	failed bool // the window could not be made at all
}

// windowOpener shows the launcher page in a window of the platform's web view.
type windowOpener func(html string, h launcherHandlers) launcherWindow

// makeLaunch is the launcher's behaviour on every platform: it ends with the
// first valid link the viewer submits or that arrives from outside, with quit
// when the viewer closes the window, or with neither when no window could be made.
func makeLaunch(open windowOpener, view func() launcherView, download, licenses func(), forget, setVpn func(string)) launchFunc {
	return func(links <-chan string) (string, bool) {
		submitted := make(chan string, 1)
		w := open(launcherHTML(view()), launcherHandlers{
			watch: func(text string) string {
				link, errText := validate(text)
				if errText == "" {
					select {
					case submitted <- link:
					default: // a second click while the first is on its way
					}
				}
				return errText
			},
			download: download,
			licenses: licenses,
			forget:   forget,
			setVpn:   setVpn,
		})
		if w.failed {
			return "", false
		}
		var link string
		select {
		case <-w.closed:
			return "", true
		case link = <-submitted:
		case link = <-links:
		}
		w.close()
		<-w.closed
		return link, false
	}
}
