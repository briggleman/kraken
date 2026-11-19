package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // Steam library heroes are JPEGs
	_ "image/png"  // and some spec authors use PNGs
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/singleflight"
)

// Banner fetching for the card image. The URL comes from a spec, which only
// an administrator can write, but the fetch is triggered by an anonymous
// request to a public page, so it is held to the same rules as any outbound
// fetch a stranger can cause: https only, public addresses only, a short
// deadline and a size cap. A Panel with no outbound internet (the LAN-first
// case) simply gets no art; the failure is cached so every render after the
// first does not wait on it again.

const (
	bannerTimeout   = 4 * time.Second
	bannerMaxBytes  = 12 << 20
	bannerMaxWidth  = 1600 // decoded art is scaled down to this before caching
	bannerTTL       = 6 * time.Hour
	bannerFailTTL   = 10 * time.Minute
	bannerMaxCached = 32
	// bannerMaxSide and bannerMaxPixels bound the decoded image. Steam's
	// library heroes are 3840×1240; anything past 8K on a side is not art.
	bannerMaxSide   = 8192
	bannerMaxPixels = 8192 * 4096
)

// errBannerAddr is a dial to an address a public page must not reach.
var errBannerAddr = errors.New("share: banner host is not a public address")

type bannerEntry struct {
	img image.Image // nil for a cached failure
	at  time.Time
}

// BannerCache fetches and keeps decoded banner images.
type BannerCache struct {
	client *http.Client
	group  singleflight.Group
	mu     sync.Mutex
	items  map[string]bannerEntry
	now    func() time.Time
}

// NewBannerCache returns a cache whose client dials public addresses only.
func NewBannerCache() *BannerCache {
	dialer := &net.Dialer{
		Timeout: bannerTimeout,
		// Control runs after name resolution, on the address actually being
		// dialled, so a name that resolves to a LAN address is refused too.
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !PublicAddr(ip) {
				return errBannerAddr
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil, // never route the fetch through an environment proxy
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   bannerTimeout,
		ResponseHeaderTimeout: bannerTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       time.Minute,
	}
	return &BannerCache{
		client: &http.Client{
			Transport: tr,
			Timeout:   bannerTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("share: too many redirects")
				}
				if req.URL.Scheme != "https" {
					return errors.New("share: redirect away from https")
				}
				return nil
			},
		},
		items: map[string]bannerEntry{},
		now:   time.Now,
	}
}

// PublicAddr reports whether ip is an address a stranger could reach and a
// public page may point at: not private, loopback, link-local, unspecified or
// multicast.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast()) &&
		// 100.64.0.0/10 (carrier-grade NAT) is not covered by IsPrivate.
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// Get returns the decoded banner at rawURL, from the cache when it is fresh.
// A nil image with a nil error never happens: no art is an error.
//
// The download belongs to the cache, not to whichever request asked first:
// it runs once per URL however many cards wait on it (a chat service's
// unfurler and its image proxy tend to arrive together), and it is detached
// from the caller's context, so a client that hangs up mid-fetch neither
// cancels the download for the others nor leaves a failure cached for ten
// minutes. A caller that gives up just stops waiting.
func (b *BannerCache) Get(ctx context.Context, rawURL string) (image.Image, error) {
	if img, ok, err := b.cached(rawURL); ok {
		return img, err
	}
	ch := b.group.DoChan(rawURL, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bannerTimeout)
		defer cancel()
		img, err := b.fetch(fctx, rawURL)
		b.mu.Lock()
		if len(b.items) >= bannerMaxCached {
			b.evictOldest()
		}
		b.items[rawURL] = bannerEntry{img: img, at: b.now()}
		b.mu.Unlock()
		return img, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(image.Image), nil
	}
}

// cached answers from the cache when it holds a fresh entry for rawURL.
func (b *BannerCache) cached(rawURL string) (image.Image, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.items[rawURL]
	if !ok {
		return nil, false, nil
	}
	ttl := bannerTTL
	if e.img == nil {
		ttl = bannerFailTTL
	}
	if b.now().Sub(e.at) >= ttl {
		return nil, false, nil
	}
	if e.img == nil {
		return nil, true, errors.New("share: banner unavailable (cached failure)")
	}
	return e.img, true, nil
}

func (b *BannerCache) evictOldest() {
	var oldest string
	var at time.Time
	for k, e := range b.items {
		if oldest == "" || e.at.Before(at) {
			oldest, at = k, e.at
		}
	}
	delete(b.items, oldest)
}

func (b *BannerCache) fetch(ctx context.Context, rawURL string) (image.Image, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("share: banner url is not https")
	}
	ctx, cancel := context.WithTimeout(ctx, bannerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Kraken-Panel (server share card)")
	req.Header.Set("Accept", "image/jpeg, image/png")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("share: banner fetch: %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
		return nil, fmt.Errorf("share: banner is %q, not an image", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bannerMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > bannerMaxBytes {
		return nil, errors.New("share: banner is larger than 12 MB")
	}
	img, err := decodeBanner(body)
	if err != nil {
		return nil, err
	}
	return shrink(img, bannerMaxWidth), nil
}

// shrink scales img down to at most maxW wide, keeping its aspect.
func shrink(img image.Image, maxW int) image.Image {
	b := img.Bounds()
	if b.Dx() <= maxW {
		return img
	}
	h := b.Dy() * maxW / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// decodeBanner decodes a downloaded banner. The byte cap does not bound the
// decoded size: a few hundred KB of a flat colour can declare 30000×30000
// pixels and ask for gigabytes, so the header is read first and anything
// larger than a real banner is refused before a pixel is allocated.
func decodeBanner(body []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("share: banner decode: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > bannerMaxSide || cfg.Height > bannerMaxSide ||
		cfg.Width*cfg.Height > bannerMaxPixels {
		return nil, fmt.Errorf("share: banner is %d×%d, larger than a banner needs to be", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("share: banner decode: %w", err)
	}
	return img, nil
}
