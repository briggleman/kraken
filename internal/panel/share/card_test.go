package share

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"203.0.113.24": true, "8.8.8.8": true, "2001:db8::1": true,
		"10.0.0.1": false, "192.168.1.1": false, "172.31.0.1": false, "127.0.0.1": false, "::1": false,
		"169.254.169.254": false, "fe80::1": false, "fc00::1": false, "0.0.0.0": false, "224.0.0.1": false,
		"100.64.0.1": false, "::ffff:192.168.1.1": false, "::ffff:203.0.113.24": true,
	} {
		if got := PublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

// The banner fetch can be triggered by an anonymous request, so it refuses
// anything but https to a public address — including a name or a redirect
// that lands on the Panel's own network.
func TestBannerCacheRefusesNonPublic(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	}))
	defer srv.Close()

	bc := NewBannerCache()
	if _, err := bc.Get(context.Background(), srv.URL+"/hero.png"); !errors.Is(err, errBannerAddr) {
		t.Fatalf("loopback https: %v, want errBannerAddr", err)
	}
	if _, err := bc.Get(context.Background(), "http://203.0.113.24/hero.png"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http: %v, want a refusal", err)
	}
	// A failure is cached, so the next card does not wait on it again.
	if _, err := bc.Get(context.Background(), srv.URL+"/hero.png"); err == nil || !strings.Contains(err.Error(), "cached failure") {
		t.Fatalf("second fetch: %v, want the cached failure", err)
	}
}

func TestShrink(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 3840, 1240))
	small := shrink(big, bannerMaxWidth)
	if b := small.Bounds(); b.Dx() != 1600 || b.Dy() != 516 {
		t.Fatalf("shrunk to %v", b)
	}
	if got := shrink(image.NewRGBA(image.Rect(0, 0, 800, 300)), bannerMaxWidth); got.Bounds().Dx() != 800 {
		t.Fatal("a small banner is not enlarged")
	}
}

func renderOK(t *testing.T, c *Card, art image.Image) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderPNG(&buf, c, art); err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	img, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != CardW || b.Dy() != CardH {
		t.Fatalf("card is %v", b)
	}
	return img
}

// Every state renders, with and without art, and text far longer than any
// real name is truncated or wrapped rather than drawn off the card.
func TestRenderPNGStates(t *testing.T) {
	art := image.NewRGBA(image.Rect(0, 0, 640, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 640; x++ {
			art.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	long := strings.Repeat("Overflowing ", 12)
	for _, st := range []string{StateRunning, StateStarting, StateStopped, StateUnreachable} {
		c := &Card{ServerName: "Saltmarrow Crossing", Game: "Windrose", State: st, AsOf: time.Unix(0, 0),
			PlayersKnown: st == StateRunning, Players: 3, MaxPlayers: 10,
			Join: []JoinValue{{Kind: JoinCode, Value: "Kq7Rw2Xp"}, {Kind: JoinPassword, Value: "tidewater"}}}
		renderOK(t, c, art)
		renderOK(t, c, nil)
	}
	renderOK(t, &Card{ServerName: long, Game: long, State: StateRunning, AsOf: time.Unix(0, 0),
		PlayersKnown: true, Players: 999, MaxPlayers: 1000,
		Join: []JoinValue{{Kind: JoinAddress, Value: "a-very-long-hostname.subdomain.example.net:27015"},
			{Kind: JoinPassword, Value: strings.Repeat("p", 64)}}}, nil)
}

// A running card lights its chip dot; a stopped one does not.
func TestRenderPNGLightFollowsState(t *testing.T) {
	c := &Card{ServerName: "S", Game: "Valheim", State: StateRunning, AsOf: time.Unix(0, 0)}
	running := renderOK(t, c, nil)
	c.State = StateStopped
	stopped := renderOK(t, c, nil)
	// The dot's centre: the chip's left edge (it is right-aligned, so it
	// depends on the label's width) plus 22px of padding and the 7px radius.
	dot := func(state string) int {
		chipW := 22 + 14 + 14 + int(measureLabel(state)) + 22
		return CardW - 64 - chipW + 22 + 7
	}
	const dotY = 56 + 23
	r1, _, _, _ := running.At(dot(StateRunning), dotY).RGBA()
	r2, _, _, _ := stopped.At(dot(StateStopped), dotY).RGBA()
	if r1>>8 < 0xe0 {
		t.Errorf("running dot red = %#x, want the gold light", r1>>8)
	}
	if r2>>8 > 0x60 {
		t.Errorf("stopped dot centre red = %#x, want an unlit ring", r2>>8)
	}
}

func measureLabel(state string) float64 {
	fs, _ := loadFonts()
	return measure(fs.face(fs.ui600, 22), strings.ToUpper(stateLabel[state]), 22*0.2)
}

func TestFitAndWrap(t *testing.T) {
	fs, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	f := fs.face(fs.mono400, 37)
	if got := fit(f, "short", 0, 1000); got != "short" {
		t.Errorf("fit kept %q", got)
	}
	got := fit(f, strings.Repeat("x", 80), 0, 300)
	if !strings.HasSuffix(got, "…") || measure(f, got, 0) > 300 {
		t.Errorf("fit = %q (%.0fpx)", got, measure(f, got, 0))
	}
	lines := wrap(fs.face(fs.ui700, 76), "The Long Dark Below the Shelf and Further Still", 0, 600, 2)
	if len(lines) != 2 {
		t.Errorf("wrap gave %d lines: %q", len(lines), lines)
	}
}

func TestPNGCache(t *testing.T) {
	p := NewPNGCache()
	now := time.Unix(1000, 0)
	p.Put("a", []byte("x"), now)
	if d, ok := p.Get("a", now.Add(time.Minute)); !ok || string(d) != "x" {
		t.Fatal("fresh entry missed")
	}
	if _, ok := p.Get("a", now.Add(pngTTL)); ok {
		t.Fatal("expired entry served")
	}
	for i := 0; i < pngMaxCached+5; i++ {
		p.Put(string(rune('A'+i)), nil, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(p.items) > pngMaxCached {
		t.Fatalf("cache grew to %d", len(p.items))
	}
}

// The cache key changes with anything the image draws, and not otherwise.
func TestCardKey(t *testing.T) {
	base := Card{ServerName: "S", Game: "G", State: StateRunning, AsOf: time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)}
	same := base
	same.AsOf = base.AsOf.Add(30 * time.Second)
	if base.Key() != same.Key() {
		t.Error("seconds within the same minute must share a render")
	}
	for name, mut := range map[string]func(*Card){
		"state":   func(c *Card) { c.State = StateStopped },
		"players": func(c *Card) { c.PlayersKnown, c.Players = true, 2 },
		"minute":  func(c *Card) { c.AsOf = c.AsOf.Add(time.Minute) },
		"join":    func(c *Card) { c.Join = []JoinValue{{Kind: JoinPassword, Value: "pw"}} },
		"name":    func(c *Card) { c.ServerName = "T" },
	} {
		c := base
		mut(&c)
		if c.Key() == base.Key() {
			t.Errorf("changing %s did not change the key", name)
		}
	}
}
