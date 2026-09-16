/* Kraken wiki — three small behaviours, no framework, no build step.
 *
 *   1. the contents drawer on narrow viewports
 *   2. copy on every command block
 *   3. the on-this-page rail tracking the section being read
 *
 * Everything here is progressive: with JS off the page still reads, the
 * nav is still a list of links, and a command block is still selectable
 * text. Nothing below draws UI that does not already exist in the HTML.
 */
(function () {
  "use strict";

  /* ---- contents drawer ---- */
  var toggle = document.getElementById("navToggle");
  var side = document.getElementById("side");
  if (toggle && side) {
    toggle.addEventListener("click", function () {
      var open = side.classList.toggle("open");
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
    });
    side.addEventListener("click", function (e) {
      if (e.target.closest("a")) {
        side.classList.remove("open");
        toggle.setAttribute("aria-expanded", "false");
      }
    });
  }

  /* ---- copy a command block ----
   * The label is the feedback: the pill says `copied` for a moment and
   * takes the light, then goes back to asking nothing. */
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    var label = btn.querySelector("span");
    var idle = label ? label.textContent : "copy";
    var timer;
    btn.addEventListener("click", function () {
      var block = btn.closest(".cmd");
      var code = block && block.querySelector("code");
      if (!code) return;
      var done = function (ok) {
        if (label) label.textContent = ok ? "copied" : "select it";
        btn.classList.add("did");
        clearTimeout(timer);
        timer = setTimeout(function () {
          if (label) label.textContent = idle;
          btn.classList.remove("did");
        }, 1600);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(code.textContent).then(
          function () {
            done(true);
          },
          function () {
            done(false);
          },
        );
      } else {
        done(false);
      }
    });
  });

  /* ---- on this page ----
   * The lit entry is the section you are in, not the one nearest the
   * top of the window: headings are observed in a band just under the
   * sticky strip, and the last one to cross it wins. */
  var links = Array.prototype.slice.call(document.querySelectorAll(".rail .toc-link"));
  if (links.length && "IntersectionObserver" in window) {
    var byId = {};
    links.forEach(function (a) {
      byId[a.getAttribute("href").slice(1)] = a;
    });
    var targets = Object.keys(byId)
      .map(function (id) {
        return document.getElementById(id);
      })
      .filter(Boolean);

    var visible = [];
    var mark = function (id) {
      links.forEach(function (a) {
        a.classList.toggle("is-here", a.getAttribute("href") === "#" + id);
      });
    };

    var io = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (en) {
          var id = en.target.id;
          var at = visible.indexOf(id);
          if (en.isIntersecting && at === -1) visible.push(id);
          if (!en.isIntersecting && at !== -1) visible.splice(at, 1);
        });
        if (!visible.length) return;
        var order = targets.map(function (t) {
          return t.id;
        });
        visible.sort(function (a, b) {
          return order.indexOf(a) - order.indexOf(b);
        });
        mark(visible[0]);
      },
      { rootMargin: "-80px 0px -70% 0px", threshold: 0 },
    );
    targets.forEach(function (t) {
      io.observe(t);
    });
  }
})();
