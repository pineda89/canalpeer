package cast

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Renderer is a DLNA MediaRenderer with an AVTransport service.
type Renderer struct {
	Name, IP   string
	ControlURL string // absolute
}

type description struct {
	URLBase string `xml:"URLBase"`
	Device  device `xml:"device"`
}

type device struct {
	FriendlyName string    `xml:"friendlyName"`
	Services     []service `xml:"serviceList>service"`
	Devices      []device  `xml:"deviceList>device"`
}

type service struct {
	Type       string `xml:"serviceType"`
	ControlURL string `xml:"controlURL"`
}

func (d device) avTransport() string {
	for _, s := range d.Services {
		if strings.HasPrefix(s.Type, "urn:schemas-upnp-org:service:AVTransport:") {
			return s.ControlURL
		}
	}
	for _, c := range d.Devices {
		if u := c.avTransport(); u != "" {
			return u
		}
	}
	return ""
}

// ParseDescription reads a device description fetched from base and returns
// the renderer; it fails when the device has no AVTransport service.
func ParseDescription(base string, b []byte) (Renderer, error) {
	var d description
	if err := xml.Unmarshal(b, &d); err != nil {
		return Renderer{}, err
	}
	ctl := d.Device.avTransport()
	if ctl == "" {
		return Renderer{}, errors.New("cast: no AVTransport service")
	}
	root, err := url.Parse(base)
	if err != nil {
		return Renderer{}, err
	}
	if d.URLBase != "" {
		if u, err := url.Parse(d.URLBase); err == nil && u.Host != "" {
			root = u
		}
	}
	ref, err := url.Parse(ctl)
	if err != nil {
		return Renderer{}, err
	}
	// The control URL is POSTed to later: it stays on the device described,
	// whatever its URLBase or an absolute controlURL say.
	from, err := url.Parse(base)
	if err != nil {
		return Renderer{}, err
	}
	control := root.ResolveReference(ref)
	if control.Hostname() != from.Hostname() {
		return Renderer{}, fmt.Errorf("cast: control URL on another host: %s", control.Host)
	}
	return Renderer{Name: strings.TrimSpace(d.Device.FriendlyName), IP: from.Hostname(), ControlURL: control.String()}, nil
}
