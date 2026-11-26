package main

import "testing"

func TestOfferReachesPlayerAttachedLater(t *testing.T) {
	up := &appUpdate{}
	up.set(offer{text: "CanalPeer 9.9.9 is available — press U to download", url: "https://x", version: "9.9.9"})
	p := newPlayer(newFakeCtl(), nil, "")
	up.attach(p)
	select {
	case o := <-p.offers:
		if o.text != "CanalPeer 9.9.9 is available — press U to download" {
			t.Fatalf("offer %q", o.text)
		}
	default:
		t.Fatal("no offer for the new player")
	}
	if text, url := up.banner(); text != "CanalPeer 9.9.9 is available" || url != "https://x" {
		t.Fatalf("banner %q %q", text, url)
	}
}

func TestOfferReachesPlayingPlayer(t *testing.T) {
	up := &appUpdate{}
	p := newPlayer(newFakeCtl(), nil, "")
	up.attach(p)
	up.set(offer{text: "Updated to CanalPeer 1.1.0"})
	if len(p.offers) != 1 {
		t.Fatal("the playing player missed the offer")
	}
}

func TestStagedUpdateIsKept(t *testing.T) {
	up := &appUpdate{}
	if up.staged() != nil {
		t.Fatal("staged before anything was staged")
	}
	up.stage(&staged{version: "1.1.0"})
	if s := up.staged(); s == nil || s.version != "1.1.0" {
		t.Fatalf("staged %v", s)
	}
	if text, _ := up.banner(); text != "CanalPeer 1.1.0 will be installed when you close the app." {
		t.Fatalf("banner %q", text)
	}
}

// "Updated to" is news once, not at the start of every channel.
func TestUpdatedNoticeShownOnce(t *testing.T) {
	up := &appUpdate{}
	up.set(offer{text: "Updated to CanalPeer 1.1.0"})
	first := newPlayer(newFakeCtl(), nil, "")
	up.attach(first)
	second := newPlayer(newFakeCtl(), nil, "")
	up.attach(second)
	if len(first.offers) != 1 || len(second.offers) != 0 {
		t.Fatalf("first %d, second %d offers", len(first.offers), len(second.offers))
	}
}
