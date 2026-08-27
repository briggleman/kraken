// Apply an inline style string through the CSSOM instead of the style
// attribute.
//
// The Panel serves this bundle under a deliberately tight CSP:
// `style-src 'self'` with NO 'unsafe-inline' (see SECURITY.md). That blocks
// literal `style="…"` attributes — including the ones a framework writes with
// setAttribute — and the block is SILENT: the declaration simply never
// applies, so instruments render at their CSS defaults and nothing errors
// except a console line. Writing through `el.style` is a CSSOM mutation,
// which CSP does not govern, so the house's `--lvl` / `--pct` / `--ox`
// grammar keeps working with the policy intact.
//
//   <i class="dotcol" use:istyle={"--lvl: " + v + "%"}></i>
//
// Prefer Svelte's `style:` directive for a single fixed property; use this
// where the value is one composed declaration string (the mock's packet and
// ray descriptors) or several properties at once.
export function istyle(node: HTMLElement | SVGElement, value: string) {
  const apply = (v: string) => {
    node.style.cssText = v ?? "";
  };
  apply(value);
  return {
    update: apply,
    destroy() {
      node.style.cssText = "";
    },
  };
}
