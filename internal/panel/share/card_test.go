package share

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"203.0.113.24": true, "8.8.8.8": true, "2001:db8::1": true,
		"10.0.0.1": false, "192.168.1.1": false, "172.31.0.1": false, "127.0.0.1": false, "::1": false,
		"169.254.169.254": false, "fe80::1": false, "fc00::1": false, "0.0.0.0": false, "224.0.0.1": false,
		"100.64.0.1": false, "::ffff:192.168.1.1": false, "::ffff:203.0.113.24": true,
		"0.1.2.3": false, "192.0.0.8": false, "198.18.0.1": false, "255.255.255.255": false, "240.0.0.1": false,
		"64:ff9b::a9fe:a9fe": false, "64:ff9b::cb00:7118": true, "64:ff9b:1::1": false,
		"2002:c0a8:0101::1": false, "2002:cb00:7118::1": true, "2001:0:4136:e378::1": false, "fec0::1": false,
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
	small := shrink(big, bannerMaxWidth, bannerMaxHeight)
	if b := small.Bounds(); b.Dx() != 1600 || b.Dy() != 516 {
		t.Fatalf("shrunk to %v", b)
	}
	if got := shrink(image.NewRGBA(image.Rect(0, 0, 800, 300)), bannerMaxWidth, bannerMaxHeight); got.Bounds().Dx() != 800 {
		t.Fatal("a small banner is not enlarged")
	}
	// A tall, narrow image is bounded by height too, and a 16-bit one is
	// cached at 8 bits, so nothing cached outgrows the two limits.
	tall := shrink(image.NewRGBA64(image.Rect(0, 0, 1200, 4000)), bannerMaxWidth, bannerMaxHeight)
	if b := tall.Bounds(); b.Dx() != 300 || b.Dy() != 1000 {
		t.Fatalf("tall banner shrunk to %v", b)
	}
	if _, ok := tall.(*image.RGBA); !ok {
		t.Fatalf("cached art is %T, want *image.RGBA", tall)
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

// A tiny file can declare an enormous image; the header is checked before a
// pixel is allocated.
func TestDecodeBannerRefusesGiantDimensions(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	ok := buf.Bytes()
	if _, err := decodeBanner(ok); err != nil {
		t.Fatalf("a 16x16 banner: %v", err)
	}
	// Rewrite the IHDR width/height (bytes 16..23) to 30000x30000; the
	// header check must refuse it without decoding.
	big := append([]byte(nil), ok...)
	for i, v := range []byte{0, 0, 0x75, 0x30, 0, 0, 0x75, 0x30} {
		big[16+i] = v
	}
	// IHDR's CRC covers its type and data (bytes 12..28) and sits at 29..32.
	binary.BigEndian.PutUint32(big[29:33], crc32.ChecksumIEEE(big[12:29]))
	if _, err := decodeBanner(big); err == nil || !strings.Contains(err.Error(), "larger than a banner") {
		t.Fatalf("30000x30000 banner: %v, want a refusal", err)
	}
}

// A caller that gives up stops waiting without cancelling the download or
// leaving a failure cached for the next card.
func TestBannerCacheCallerCancelDoesNotPoison(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 8, 4)))
	}))
	defer srv.Close()
	bc := NewBannerCache()
	bc.client = srv.Client() // the test server is on loopback; skip the public-address dialer

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := bc.Get(ctx, srv.URL+"/hero.png")
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v, want context.Canceled", err)
	}
	close(release)
	img, err := bc.Get(context.Background(), srv.URL+"/hero.png")
	if err != nil || img.Bounds().Dx() != 8 {
		t.Fatalf("after a cancelled caller: %v, %v — want the banner, not a cached failure", img, err)
	}
}

// Cards are drawn per request, and a posted link draws several at once (a
// chat's unfurler and its image proxy, or two links rendering together). The
// font faces are cached and an x/image Face is not safe for concurrent use,
// so this runs renders in parallel for -race to see any sharing.
func TestRenderPNGConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &Card{ServerName: "Saltmarrow Crossing " + strconv.Itoa(i), Game: "Windrose", State: StateRunning,
				Players: i, MaxPlayers: 8, PlayersKnown: true, AsOf: time.Unix(0, 0),
				Join: []JoinValue{{Kind: JoinAddress, Value: "play.example.com:7777"}}}
			var buf bytes.Buffer
			errs <- RenderPNG(&buf, c, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
