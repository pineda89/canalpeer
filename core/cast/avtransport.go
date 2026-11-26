package cast

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const avtNS = "urn:schemas-upnp-org:service:AVTransport:1"

const protocolInfo = "http-get:*:video/mp2t:DLNA.ORG_OP=00;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func soap(ctx context.Context, controlURL, action, args string) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + action + ` xmlns:u="` + avtNS + `"><InstanceID>0</InstanceID>` + args + `</u:` + action + `></s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, "POST", controlURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+avtNS+`#`+action+`"`)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		var f struct {
			Code string `xml:"Body>Fault>detail>UPnPError>errorCode"`
			Desc string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
		}
		if xml.Unmarshal(b, &f) == nil && f.Code != "" {
			return "", fmt.Errorf("cast: %s: UPnP error %s %s", action, f.Code, f.Desc)
		}
		return "", fmt.Errorf("cast: %s: %s: %.200s", action, resp.Status, b)
	}
	return string(b), nil
}

// SetURI points the renderer at a live MPEG-TS stream.
func SetURI(ctx context.Context, controlURL, mediaURL, title string) error {
	didl := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"><item id="0" parentID="-1" restricted="1"><dc:title>` + esc(title) + `</dc:title><upnp:class>object.item.videoItem</upnp:class><res protocolInfo="` + protocolInfo + `">` + esc(mediaURL) + `</res></item></DIDL-Lite>`
	_, err := soap(ctx, controlURL, "SetAVTransportURI", "<CurrentURI>"+esc(mediaURL)+"</CurrentURI><CurrentURIMetaData>"+esc(didl)+"</CurrentURIMetaData>")
	return err
}

func Play(ctx context.Context, controlURL string) error {
	_, err := soap(ctx, controlURL, "Play", "<Speed>1</Speed>")
	return err
}

func Stop(ctx context.Context, controlURL string) error {
	_, err := soap(ctx, controlURL, "Stop", "")
	return err
}

// State returns CurrentTransportState (PLAYING, STOPPED, TRANSITIONING...).
func State(ctx context.Context, controlURL string) (string, error) {
	resp, err := soap(ctx, controlURL, "GetTransportInfo", "")
	if err != nil {
		return "", err
	}
	var env struct {
		State string `xml:"Body>GetTransportInfoResponse>CurrentTransportState"`
	}
	if err := xml.Unmarshal([]byte(resp), &env); err != nil {
		return "", err
	}
	return env.State, nil
}
