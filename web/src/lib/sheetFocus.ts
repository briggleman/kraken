// Focus lands only once the plunge finishes and the sheet is really visible
// (the overlay is mid-transition on open). Attach to every .sheet/.prefs
// element; fires on the clip-path transitionend while open.
//
// NOT a bare `button` selector: with appearance: base-select a <select>
// holds a real <button> in the DOM, and focusing that internal button does
// nothing at all. Asking for .cfg-btn asks for one of ours; preferring
// .solid lands on the step's commitment rather than on `back`. The wizard
// deliberately has no .surface-btn; there the first field of the open step
// is the honest landing place.
export function sheetFocus(el: HTMLElement) {
  function onEnd(e: TransitionEvent) {
    if (e.target !== el || e.propertyName !== "clip-path" || !el.classList.contains("open"))
      return;
    const panel = ".wz-panel.on ";
    const first =
      el.querySelector<HTMLElement>(".surface-btn") ||
      el.querySelector<HTMLElement>(panel + 'input:not([type="radio"]):not([type="checkbox"])') ||
      el.querySelector<HTMLElement>(panel + ".cfg-btn.solid:not([disabled])") ||
      el.querySelector<HTMLElement>(panel + ".cfg-btn:not([disabled])");
    if (first) first.focus();
  }
  el.addEventListener("transitionend", onEnd);
  return {
    destroy() {
      el.removeEventListener("transitionend", onEnd);
    },
  };
}
