// The share page's two controls (#475): copy a join value, and show or hide the
// password. Without this script the page still shows every value as plain
// text, and the buttons stay hidden rather than sitting there doing nothing.
(function () {
  "use strict";
  var MASK = "•••••••••";
  var CHECK = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>';

  function ready(fn) {
    if (document.readyState !== "loading") fn();
    else document.addEventListener("DOMContentLoaded", fn);
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    // Plain-HTTP Panels have no clipboard API: fall back to a hidden textarea.
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      if (ok) resolve(); else reject(new Error("copy failed"));
    });
  }

  ready(function () {
    document.querySelectorAll("[data-secret]").forEach(function (el) {
      el.dataset.value = el.textContent;
      el.textContent = MASK;
      el.classList.add("masked");
    });
    document.querySelectorAll("[data-copy], [data-reveal]").forEach(function (b) { b.hidden = false; });

    document.addEventListener("click", function (e) {
      var c = e.target.closest("[data-copy]");
      if (c) {
        var label = c.querySelector("span");
        var icon = c.querySelector("svg");
        var was = label ? label.textContent : "";
        var iconHTML = icon ? icon.outerHTML : "";
        copyText(c.dataset.copy).then(function () {
          c.classList.add("done");
          if (icon) icon.outerHTML = CHECK;
          if (label) label.textContent = "Copied";
          setTimeout(function () {
            c.classList.remove("done");
            var now = c.querySelector("svg");
            if (now && iconHTML) now.outerHTML = iconHTML;
            if (label) label.textContent = was;
          }, 1600);
        }, function () {
          if (label) label.textContent = "Select and copy";
        });
        return;
      }
      var r = e.target.closest("[data-reveal]");
      if (r) {
        var v = r.closest(".row").querySelector("[data-secret]");
        var on = r.getAttribute("aria-pressed") !== "true";
        r.setAttribute("aria-pressed", String(on));
        r.setAttribute("aria-label", on ? "Hide password" : "Show password");
        v.textContent = on ? v.dataset.value : MASK;
        v.classList.toggle("masked", !on);
      }
    });
  });
})();
