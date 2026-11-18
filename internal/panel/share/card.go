package share

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The card image (#475): the 1200×630 og:image a chat service shows under a
// shared link, drawn in pure Go so the Panel stays two binaries and Postgres —
// no headless browser. It follows design/mockups/server-share-card.html: the
// game's art sunk into the water column, the server name as the label, the
// game as the headline, a status chip, a rail of readings, and an as-of stamp.
//
// A chat service shows the image at about a third of its size, so every type
// size here is chosen to hold at ~400px wide.

// CardW and CardH are the card's size, Open Graph's large-image ratio.
const (
	CardW = 1200
	CardH = 630
)

//go:embed fonts/archivo-600.ttf
var archivo600 []byte

//go:embed fonts/archivo-700.ttf
var archivo700 []byte

//go:embed fonts/spline-sans-mono-300.ttf
var mono300 []byte

//go:embed fonts/spline-sans-mono-400.ttf
var mono400 []byte

//go:embed fonts/spline-sans-mono-500.ttf
var mono500 []byte

//go:embed assets/kraken-glyph-teal.png
var glyphPNG []byte

// The palette, from the Abyssal tokens.
var (
	cVoid     = color.RGBA{0x02, 0x09, 0x0e, 0xff}
	cD1       = color.RGBA{0x07, 0x15, 0x21, 0xff}
	cD2       = color.RGBA{0x0a, 0x1e, 0x2c, 0xff}
	cLumen    = color.RGBA{0xff, 0xc2, 0x66, 0xff}
	cTeal     = color.RGBA{0x1a, 0x7a, 0x6d, 0xff}
	cViolet   = color.RGBA{0x9d, 0x8c, 0xff, 0xff}
	cInk      = color.RGBA{0xf5, 0xdf, 0xc1, 0xff}
	cInk2     = color.RGBA{0xb6, 0xa8, 0x94, 0xff}
	cInk3     = color.RGBA{0x7d, 0x71, 0x60, 0xff}
	waterStop = []struct {
		at float64
		c  color.RGBA
	}{{0, color.RGBA{0x0d, 0x33, 0x40, 0xff}}, {0.34, color.RGBA{0x08, 0x25, 0x30, 0xff}}, {0.64, color.RGBA{0x04, 0x14, 0x1b, 0xff}}, {1, cVoid}}
)

type fontSet struct {
	ui600, ui700              *opentype.Font
	mono300, mono400, mono500 *opentype.Font
	glyph                     *image.Alpha
	faces                     map[faceKey]font.Face
	mu                        sync.Mutex
}

type faceKey struct {
	f    *opentype.Font
	size float64
}

var (
	fontsOnce sync.Once
	fonts     *fontSet
	fontsErr  error
)

func loadFonts() (*fontSet, error) {
	fontsOnce.Do(func() {
		fs := &fontSet{faces: map[faceKey]font.Face{}}
		for _, x := range []struct {
			dst **opentype.Font
			src []byte
		}{{&fs.ui600, archivo600}, {&fs.ui700, archivo700}, {&fs.mono300, mono300}, {&fs.mono400, mono400}, {&fs.mono500, mono500}} {
			f, err := opentype.Parse(x.src)
			if err != nil {
				fontsErr = err
				return
			}
			*x.dst = f
		}
		g, err := png.Decode(bytes.NewReader(glyphPNG))
		if err != nil {
			fontsErr = err
			return
		}
		a := image.NewAlpha(g.Bounds())
		xdraw.Draw(a, a.Bounds(), g, g.Bounds().Min, xdraw.Src)
		fs.glyph = a
		fonts = fs
	})
	return fonts, fontsErr
}

func (fs *fontSet) face(f *opentype.Font, size float64) font.Face {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k := faceKey{f, size}
	if fc, ok := fs.faces[k]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err) // a compiled-in font that will not size is a build bug
	}
	fs.faces[k] = fc
	return fc
}

// cardLook is the per-state treatment of the art and the readings.
type cardLook struct {
	artSat, artBright float64
	surface           float64 // strength of the top light
	headline, values  color.RGBA
	glow              bool
	chipText          color.RGBA
	chipBorder        color.RGBA
	chipFill          color.RGBA
	chipDot           bool // a lit dot; otherwise a ring
	occ               bool // the occupancy fill is lit
	stamp             color.RGBA
}

func lookFor(state string) cardLook {
	switch state {
	case StateRunning:
		return cardLook{artSat: 0.72, artBright: 0.78, surface: 1, headline: cInk, values: cInk, glow: true,
			chipText: cLumen, chipBorder: withA(cLumen, 0.45), chipFill: withA(cLumen, 0.12), chipDot: true, occ: true, stamp: cInk2}
	case StateUnreachable:
		return cardLook{artSat: 0.3, artBright: 0.55, surface: 0.45, headline: cInk, values: cInk2,
			chipText: cViolet, chipBorder: withA(cViolet, 0.5), chipFill: withA(cVoid, 0.55), stamp: cViolet}
	default: // stopped, starting: the light is out
		return cardLook{artSat: 0, artBright: 0.5, surface: 0.3, headline: cInk2, values: cInk2,
			chipText: cInk2, chipBorder: withA(cInk3, 0.55), chipFill: withA(cVoid, 0.55), stamp: cInk2}
	}
}

// RenderPNG draws the card as a PNG. art may be nil (no banner, or the fetch
// failed); the card is then the plain water column.
func RenderPNG(w io.Writer, c *Card, art image.Image) error {
	fs, err := loadFonts()
	if err != nil {
		return err
	}
	look := lookFor(c.State)
	img := image.NewRGBA(image.Rect(0, 0, CardW, CardH))

	// The water column.
	for y := 0; y < CardH; y++ {
		col := gradientAt(float64(y) / float64(CardH-1))
		for x := 0; x < CardW; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	if art != nil {
		drawArt(img, art, look)
	}
	// The light from above, then the vignette that frames the card.
	radial(img, 0.30*CardW, -0.12*CardH, 0.9*CardW, 0.7*CardH, color.RGBA{0xff, 0xd7, 0x9a, 0xff}, 0.14*look.surface)
	vignette(img)

	const left, right, top, bottom = 64, CardW - 64, 56, CardH - 48

	// Top row: the server's name as the label, the status chip at the right.
	chipLabel := strings.ToUpper(c.StateLabel())
	chipFace := fs.face(fs.ui600, 22)
	chipW := 22 + 14 + 14 + int(measure(chipFace, chipLabel, 22*0.2)) + 22
	chipH := 46
	chipX := right - chipW
	roundRect(img, float64(chipX), top, float64(chipW), float64(chipH), 5, look.chipFill)
	strokeRect(img, float64(chipX), top, float64(chipW), float64(chipH), 5, 1.5, look.chipBorder)
	dotCX, dotCY := float64(chipX+22+7), float64(top+chipH/2)
	if look.chipDot {
		glowDisc(img, dotCX, dotCY, 7, 18, look.chipText)
		disc(img, dotCX, dotCY, 7, look.chipText)
	} else {
		ring(img, dotCX, dotCY, 7, 2.5, look.chipText)
	}
	drawText(img, chipFace, float64(chipX+22+14+14), float64(top+chipH/2)+8, chipLabel, 22*0.2, look.chipText)

	srvFace := fs.face(fs.ui600, 24)
	srvMax := float64(chipX - left - 40)
	srv := fit(srvFace, strings.ToUpper(c.ServerName), 24*0.22, srvMax)
	srvW := measure(srvFace, srv, 24*0.22)
	drawText(img, srvFace, left, float64(top+chipH/2)+8, srv, 24*0.22, cInk)
	hair := math.Min(srvW+56, srvMax)
	fillRect(img, left, float64(top+chipH/2)+22, hair, 1.5, withA(cLumen, 0.2))

	// The rail of readings and the footer, laid out from the bottom up.
	const footH = 42
	railH := 158.0
	railY := float64(bottom - footH - int(railH))
	drawRail(img, fs, c, look, left, railY, right-left, railH)

	// The headline: the game, centred between the top row and the rail.
	drawHeadline(img, fs, c.Game, look, left, float64(top+chipH), railY)

	// Footer: the brand at the left, the as-of stamp at the right.
	footY := float64(bottom) - 8
	glyphH := 34.0
	glyphCol := cLumen
	if !look.glow {
		glyphCol = cInk3
	}
	drawGlyph(img, fs.glyph, left, footY-glyphH+6, glyphH, glyphCol)
	brandFace := fs.face(fs.ui700, 24)
	drawText(img, brandFace, left+42, footY, "KRAKEN", 24*0.2, cInk2)
	stampFace := fs.face(fs.mono300, 24)
	stampVal := fs.face(fs.mono400, 24)
	prefix := "as of "
	if c.State == StateUnreachable {
		prefix = "last checked "
	}
	val := c.AsOfText()
	wPre := measure(stampFace, prefix, 0)
	wVal := measure(stampVal, val, 0)
	sx := float64(right) - wPre - wVal
	drawText(img, stampFace, sx, footY, prefix, 0, cInk3)
	drawText(img, stampVal, sx+wPre, footY, val, 0, look.stamp)

	return png.Encode(w, img)
}

// drawHeadline sets the game's name as large as fits: 112px on one line,
// else 84px on up to two.
func drawHeadline(img *image.RGBA, fs *fontSet, game string, look cardLook, left, top, bottom float64) {
	maxW := float64(CardW) * 0.62
	for _, size := range []float64{112, 96, 84} {
		f := fs.face(fs.ui700, size)
		if measure(f, game, size*0.02) <= maxW {
			y := (top+bottom)/2 + size*0.36
			shadowText(img, f, left, y+4, game, size*0.02)
			drawText(img, f, left, y, game, size*0.02, look.headline)
			return
		}
	}
	size := 76.0
	f := fs.face(fs.ui700, size)
	lines := wrap(f, game, size*0.02, maxW, 2)
	lh := size * 1.02
	y0 := (top+bottom)/2 - lh*float64(len(lines)-1)/2 + size*0.36
	for i, ln := range lines {
		shadowText(img, f, left, y0+float64(i)*lh+4, ln, size*0.02)
		drawText(img, f, left, y0+float64(i)*lh, ln, size*0.02, look.headline)
	}
}

// drawRail draws the panel of readings: players, then each join value.
func drawRail(img *image.RGBA, fs *fontSet, c *Card, look cardLook, x, y, w, h float64) {
	// The panel: a top-lit gradient, a hairline edge and the inner top light.
	roundRectGrad(img, x, y, w, h, 8, withA(cD2, 0.94), withA(cD1, 0.94))
	strokeRect(img, x, y, w, h, 8, 1.5, withA(cLumen, 0.16))
	fillRect(img, x+8, y+1.5, w-16, 1.5, withA(cLumen, 0.08))

	label := fs.face(fs.ui600, 17)
	big := fs.face(fs.mono500, 62)
	small := fs.face(fs.mono300, 31)
	joinF := fs.face(fs.mono400, 37)

	px, labelY := x+32, y+24+17
	bigY := labelY + 10 + 52
	drawText(img, label, px, labelY, "PLAYERS", 17*0.26, cInk3)
	num, capText := "–", ""
	if c.PlayersKnown {
		num = itoa(c.Players)
		if c.MaxPlayers > 0 {
			capText = " / " + itoa(c.MaxPlayers)
		}
	} else if c.MaxPlayers > 0 {
		capText = " / " + itoa(c.MaxPlayers)
	}
	if look.glow && c.PlayersKnown {
		glowText(img, big, px, bigY, num, 0, cLumen, 14, 0.5)
	}
	drawText(img, big, px, bigY, num, 0, look.values)
	drawText(img, small, px+measure(big, num, 0), bigY, capText, 0, cInk2)
	occY := bigY + 18
	fillRoundRect(img, px, occY, 180, 9, 4.5, withA(cInk3, 0.22))
	if c.PlayersKnown && c.MaxPlayers > 0 {
		fw := 180 * math.Min(1, float64(c.Players)/float64(c.MaxPlayers))
		if fw > 0 {
			if look.occ {
				roundRectHGrad(img, px, occY, fw, 9, 4.5, cTeal, cLumen)
			} else {
				fillRoundRect(img, px, occY, fw, 9, 4.5, withA(cInk3, 0.6))
			}
		}
	}

	colX := px + math.Max(180, measure(big, num, 0)+measure(small, capText, 0)) + 30
	joinY := bigY - 4
	for _, j := range c.Join {
		avail := x + w - 32 - (colX + 30)
		if avail < 120 {
			break
		}
		fillRect(img, colX, y+26, 1.5, h-52, withA(cLumen, 0.1))
		vx := colX + 30
		maxV := avail
		if j.Secret() {
			maxV = math.Min(maxV, measure(joinF, "MMMMMMMMMMMM", 0))
		}
		v := fit(joinF, j.Value, 0, maxV)
		drawText(img, label, vx, joinY-37-10, strings.ToUpper(j.Label()), 17*0.26, cInk3)
		drawText(img, joinF, vx, joinY, v, 0, look.values)
		colX = vx + measure(joinF, v, 0) + 30
	}
}

// ---- text ----

// measure is the advance width of s with tracking px added after each rune.
func measure(f font.Face, s string, tracking float64) float64 {
	var w fixed.Int26_6
	prev := rune(-1)
	n := 0
	for _, r := range s {
		if prev >= 0 {
			w += f.Kern(prev, r)
		}
		a, _ := f.GlyphAdvance(r)
		w += a
		prev = r
		n++
	}
	return float64(w)/64 + tracking*float64(max(n-1, 0))
}

// fit truncates s with an ellipsis until it measures within maxW.
func fit(f font.Face, s string, tracking, maxW float64) string {
	if measure(f, s, tracking) <= maxW {
		return s
	}
	rs := []rune(s)
	for len(rs) > 1 {
		rs = rs[:len(rs)-1]
		t := strings.TrimRight(string(rs), " ") + "…"
		if measure(f, t, tracking) <= maxW {
			return t
		}
	}
	return "…"
}

// wrap breaks s at spaces into at most n lines within maxW; the last line is
// truncated if words remain.
func wrap(f font.Face, s string, tracking, maxW float64, n int) []string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for i, w := range words {
		try := strings.TrimSpace(cur + " " + w)
		if cur == "" || measure(f, try, tracking) <= maxW {
			cur = try
			continue
		}
		if len(lines) == n-1 {
			cur = strings.Join(append([]string{cur}, words[i:]...), " ")
			break
		}
		lines = append(lines, cur)
		cur = w
	}
	lines = append(lines, fit(f, cur, tracking, maxW))
	return lines
}

// drawText draws s with its baseline at y, adding tracking px after each rune.
func drawText(img *image.RGBA, f font.Face, x, y float64, s string, tracking float64, col color.RGBA) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: f}
	pos := fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			pos.X += f.Kern(prev, r)
		}
		d.Dot = pos
		d.DrawString(string(r))
		a, _ := f.GlyphAdvance(r)
		pos.X += a + fixed.Int26_6(tracking*64)
		prev = r
	}
}

// textMask renders s into an alpha mask the size of the card.
func textMask(f font.Face, x, y float64, s string, tracking float64) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, CardW, CardH))
	d := &font.Drawer{Dst: m, Src: image.Opaque, Face: f}
	pos := fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			pos.X += f.Kern(prev, r)
		}
		d.Dot = pos
		d.DrawString(string(r))
		a, _ := f.GlyphAdvance(r)
		pos.X += a + fixed.Int26_6(tracking*64)
		prev = r
	}
	return m
}

// glowText lays a blurred copy of s under where it will be drawn.
func glowText(img *image.RGBA, f font.Face, x, y float64, s string, tracking float64, col color.RGBA, radius int, strength float64) {
	m := textMask(f, x, y, s, tracking)
	blurAlpha(m, radius)
	paintMask(img, m, col, strength)
}

// shadowText lays a soft dark shadow under a headline over the art.
func shadowText(img *image.RGBA, f font.Face, x, y float64, s string, tracking float64) {
	m := textMask(f, x, y, s, tracking)
	blurAlpha(m, 16)
	paintMask(img, m, cVoid, 0.7)
}

// ---- shapes ----

func withA(c color.RGBA, a float64) color.RGBA {
	c.A = uint8(math.Round(a * 255))
	return c
}

// blend paints col at coverage cov (0–1) over the pixel at x, y.
func blend(img *image.RGBA, x, y int, col color.RGBA, cov float64) {
	if cov <= 0 || x < 0 || y < 0 || x >= CardW || y >= CardH {
		return
	}
	a := float64(col.A) / 255 * math.Min(cov, 1)
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+4 : i+4]
	p[0] = uint8(float64(p[0])*(1-a) + float64(col.R)*a + 0.5)
	p[1] = uint8(float64(p[1])*(1-a) + float64(col.G)*a + 0.5)
	p[2] = uint8(float64(p[2])*(1-a) + float64(col.B)*a + 0.5)
	p[3] = 255
}

func paintMask(img *image.RGBA, m *image.Alpha, col color.RGBA, strength float64) {
	for y := 0; y < CardH; y++ {
		for x := 0; x < CardW; x++ {
			if a := m.Pix[m.PixOffset(x, y)]; a > 0 {
				blend(img, x, y, col, float64(a)/255*strength)
			}
		}
	}
}

// rrDist is the signed distance from (px, py) to a rounded rectangle.
func rrDist(px, py, x, y, w, h, r float64) float64 {
	cx, cy := x+w/2, y+h/2
	qx := math.Abs(px-cx) - (w/2 - r)
	qy := math.Abs(py-cy) - (h/2 - r)
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

func shapeLoop(x, y, w, h, pad float64, fn func(px, py int, fx, fy float64)) {
	x0, y0 := int(math.Floor(x-pad)), int(math.Floor(y-pad))
	x1, y1 := int(math.Ceil(x+w+pad)), int(math.Ceil(y+h+pad))
	for py := max(y0, 0); py < min(y1, CardH); py++ {
		for px := max(x0, 0); px < min(x1, CardW); px++ {
			fn(px, py, float64(px)+0.5, float64(py)+0.5)
		}
	}
}

func roundRect(img *image.RGBA, x, y, w, h, r float64, col color.RGBA) {
	shapeLoop(x, y, w, h, 1, func(px, py int, fx, fy float64) {
		blend(img, px, py, col, 0.5-rrDist(fx, fy, x, y, w, h, r))
	})
}

func fillRoundRect(img *image.RGBA, x, y, w, h, r float64, col color.RGBA) {
	roundRect(img, x, y, w, h, math.Min(r, math.Min(w, h)/2), col)
}

func roundRectGrad(img *image.RGBA, x, y, w, h, r float64, top, bot color.RGBA) {
	shapeLoop(x, y, w, h, 1, func(px, py int, fx, fy float64) {
		t := math.Max(0, math.Min(1, (fy-y)/h))
		blend(img, px, py, lerp(top, bot, t), 0.5-rrDist(fx, fy, x, y, w, h, r))
	})
}

func roundRectHGrad(img *image.RGBA, x, y, w, h, r float64, l, rc color.RGBA) {
	r = math.Min(r, math.Min(w, h)/2)
	shapeLoop(x, y, w, h, 1, func(px, py int, fx, fy float64) {
		t := math.Max(0, math.Min(1, (fx-x)/math.Max(w, 1)))
		blend(img, px, py, lerp(l, rc, t), 0.5-rrDist(fx, fy, x, y, w, h, r))
	})
}

func strokeRect(img *image.RGBA, x, y, w, h, r, sw float64, col color.RGBA) {
	shapeLoop(x, y, w, h, sw+1, func(px, py int, fx, fy float64) {
		d := rrDist(fx, fy, x+sw/2, y+sw/2, w-sw, h-sw, math.Max(r-sw/2, 0))
		blend(img, px, py, col, sw/2+0.5-math.Abs(d))
	})
}

func fillRect(img *image.RGBA, x, y, w, h float64, col color.RGBA) {
	roundRect(img, x, y, w, h, 0, col)
}

func disc(img *image.RGBA, cx, cy, r float64, col color.RGBA) {
	shapeLoop(cx-r, cy-r, 2*r, 2*r, 1, func(px, py int, fx, fy float64) {
		blend(img, px, py, col, r+0.5-math.Hypot(fx-cx, fy-cy))
	})
}

func ring(img *image.RGBA, cx, cy, r, sw float64, col color.RGBA) {
	shapeLoop(cx-r, cy-r, 2*r, 2*r, 1, func(px, py int, fx, fy float64) {
		d := math.Hypot(fx-cx, fy-cy) - (r - sw/2)
		blend(img, px, py, col, sw/2+0.5-math.Abs(d))
	})
}

// glowDisc is a soft halo of radius reach around a dot of radius r.
func glowDisc(img *image.RGBA, cx, cy, r, reach float64, col color.RGBA) {
	shapeLoop(cx-reach, cy-reach, 2*reach, 2*reach, 0, func(px, py int, fx, fy float64) {
		d := math.Hypot(fx-cx, fy-cy)
		if d < reach {
			t := 1 - (d-r)/(reach-r)
			blend(img, px, py, col, 0.55*math.Max(0, math.Min(1, t))*math.Max(0, math.Min(1, t)))
		}
	})
}

// ---- backdrop ----

func lerp(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t + 0.5),
		uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t + 0.5),
		uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t + 0.5),
		uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t + 0.5),
	}
}

func gradientAt(t float64) color.RGBA {
	for i := 1; i < len(waterStop); i++ {
		if t <= waterStop[i].at {
			a, b := waterStop[i-1], waterStop[i]
			return lerp(a.c, b.c, (t-a.at)/(b.at-a.at))
		}
	}
	return cVoid
}

// radial adds a soft elliptical light centred at (cx, cy).
func radial(img *image.RGBA, cx, cy, rx, ry float64, col color.RGBA, strength float64) {
	if strength <= 0 {
		return
	}
	for y := 0; y < CardH; y++ {
		for x := 0; x < CardW; x++ {
			d := math.Hypot((float64(x)-cx)/rx, (float64(y)-cy)/ry)
			if d < 1 {
				blend(img, x, y, col, strength*(1-d)*(1-d))
			}
		}
	}
}

// vignette darkens toward every edge, as the mock's inset shadow does.
func vignette(img *image.RGBA) {
	for y := 0; y < CardH; y++ {
		for x := 0; x < CardW; x++ {
			e := math.Min(math.Min(float64(x), float64(CardW-1-x)), math.Min(float64(y), float64(CardH-1-y)))
			if e < 220 {
				t := 1 - e/220
				blend(img, x, y, color.RGBA{0x01, 0x09, 0x0e, 0xff}, 0.75*t*t)
			}
		}
	}
}

// drawArt sinks the banner into the top right of the card: it covers 82% of
// the width and 78% of the height, fades out toward the text side and toward
// the floor, is cooled and dimmed per state, and multiplied with the water
// colour so the gold stays the only warm light.
func drawArt(img *image.RGBA, art image.Image, look cardLook) {
	aw, ah := CardW*82/100, CardH*78/100
	ax := CardW - aw
	scaled := image.NewRGBA(image.Rect(0, 0, aw, ah))
	b := art.Bounds()
	// Cover: crop the source to the target's aspect, biased to 30% from the top.
	srcW, srcH := b.Dx(), b.Dy()
	cropW, cropH := srcW, srcW*ah/aw
	if cropH > srcH {
		cropH, cropW = srcH, srcH*aw/ah
	}
	cx := b.Min.X + (srcW-cropW)/2
	cy := b.Min.Y + int(float64(srcH-cropH)*0.3)
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), art, image.Rect(cx, cy, cx+cropW, cy+cropH), xdraw.Src, nil)

	for y := 0; y < ah; y++ {
		my := 1.0
		if fy := float64(y) / float64(ah); fy > 0.35 {
			my = 1 - (fy-0.35)/0.65
		}
		tint := gradientAt(float64(y) / float64(CardH))
		for x := 0; x < aw; x++ {
			mx := math.Min(1, float64(x)/(0.48*float64(aw)))
			cov := mx * my
			if cov <= 0 {
				continue
			}
			i := scaled.PixOffset(x, y)
			r, g, bl := float64(scaled.Pix[i]), float64(scaled.Pix[i+1]), float64(scaled.Pix[i+2])
			lum := 0.299*r + 0.587*g + 0.114*bl
			r = lum + (r-lum)*look.artSat
			g = lum + (g-lum)*look.artSat
			bl = lum + (bl-lum)*look.artSat
			// Brightness, then a multiply toward the water colour (the mock's
			// tint layer), which keeps the art cold.
			r = r * look.artBright * (0.62 + 0.38*float64(tint.R)/255*4)
			g = g * look.artBright * (0.62 + 0.38*float64(tint.G)/255*4)
			bl = bl * look.artBright * (0.62 + 0.38*float64(tint.B)/255*4)
			col := color.RGBA{clamp8(r), clamp8(g), clamp8(bl), 0xff}
			blend(img, ax+x, y, col, cov)
		}
	}
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// drawGlyph paints the Kraken mark, h px tall, in col.
func drawGlyph(img *image.RGBA, g *image.Alpha, x, y, h float64, col color.RGBA) {
	b := g.Bounds()
	w := h * float64(b.Dx()) / float64(b.Dy())
	m := image.NewAlpha(image.Rect(0, 0, int(math.Ceil(w)), int(math.Ceil(h))))
	xdraw.CatmullRom.Scale(m, m.Bounds(), g, b, xdraw.Src, nil)
	for py := 0; py < m.Bounds().Dy(); py++ {
		for px := 0; px < m.Bounds().Dx(); px++ {
			if a := m.Pix[m.PixOffset(px, py)]; a > 0 {
				blend(img, int(x)+px, int(y)+py, col, float64(a)/255)
			}
		}
	}
}

// blurAlpha is three box blurs, a cheap Gaussian, over the whole mask.
func blurAlpha(m *image.Alpha, r int) {
	if r < 1 {
		return
	}
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	tmp := make([]float64, w*h)
	buf := make([]float64, w*h)
	for i, v := range m.Pix {
		buf[i] = float64(v)
	}
	for pass := 0; pass < 3; pass++ {
		boxH(buf, tmp, w, h, r)
		boxV(tmp, buf, w, h, r)
	}
	for i, v := range buf {
		m.Pix[i] = clamp8(v)
	}
}

func boxH(src, dst []float64, w, h, r int) {
	inv := 1 / float64(2*r+1)
	for y := 0; y < h; y++ {
		row := y * w
		var acc float64
		for x := -r; x <= r; x++ {
			acc += src[row+clampI(x, 0, w-1)]
		}
		for x := 0; x < w; x++ {
			dst[row+x] = acc * inv
			acc += src[row+clampI(x+r+1, 0, w-1)] - src[row+clampI(x-r, 0, w-1)]
		}
	}
}

func boxV(src, dst []float64, w, h, r int) {
	inv := 1 / float64(2*r+1)
	for x := 0; x < w; x++ {
		var acc float64
		for y := -r; y <= r; y++ {
			acc += src[clampI(y, 0, h-1)*w+x]
		}
		for y := 0; y < h; y++ {
			dst[y*w+x] = acc * inv
			acc += src[clampI(y+r+1, 0, h-1)*w+x] - src[clampI(y-r, 0, h-1)*w+x]
		}
	}
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
