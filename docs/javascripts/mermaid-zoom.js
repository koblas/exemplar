// Click a Mermaid diagram to open it in an overlay, sized to fit the window,
// with zoom (+ / − / Fit buttons, Ctrl+wheel, the + − 0 keys). Esc, the ×
// button or a click on the backdrop closes it. The site renders Mermaid in
// the browser (window.mermaid) into a closed shadow root, so the rendered
// SVG can't be cloned; instead the diagram's source is kept and rendered
// again.
(function () {
  "use strict";

  var ZOOM_STEP = 1.25;
  var ZOOM_MIN = 0.5;
  var ZOOM_MAX = 8;

  // The Mermaid source of each diagram, in document order. The theme turns
  // every <pre class="mermaid"> into a <div class="mermaid"> in place, so
  // index n of one list is index n of the other.
  var sources = null;

  function textOf(nodes) {
    return Array.prototype.map.call(nodes, function (p) {
      return p.textContent;
    });
  }

  function collectSources() {
    if (sources) return Promise.resolve(sources);
    var pres = document.querySelectorAll("pre.mermaid");
    if (pres.length > 0) {
      sources = textOf(pres);
      return Promise.resolve(sources);
    }
    // Already rendered: read the page's own HTML for the sources.
    return fetch(location.href, { credentials: "same-origin" })
      .then(function (r) {
        return r.text();
      })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        sources = textOf(doc.querySelectorAll("pre.mermaid"));
        return sources;
      });
  }

  var overlay = null;
  var counter = 0;

  function el(tag, className, text) {
    var e = document.createElement(tag);
    e.className = className;
    if (text) e.textContent = text;
    return e;
  }

  function button(label, title, onClick) {
    var b = el("button", "mermaid-zoom__btn", label);
    b.type = "button";
    b.title = title;
    b.setAttribute("aria-label", title);
    b.addEventListener("click", onClick);
    return b;
  }

  function closeOverlay() {
    if (!overlay) return;
    overlay.remove();
    overlay = null;
    document.body.classList.remove("mermaid-zoom-open");
    document.removeEventListener("keydown", onKey);
  }

  var zoom = null; // {set: fn(scale), by: fn(factor), fit: fn()}

  function onKey(e) {
    if (e.key === "Escape") closeOverlay();
    if (!zoom) return;
    if (e.key === "+" || e.key === "=") zoom.by(ZOOM_STEP);
    if (e.key === "-" || e.key === "_") zoom.by(1 / ZOOM_STEP);
    if (e.key === "0") zoom.fit();
  }

  function openOverlay(source) {
    closeOverlay();
    overlay = el("div", "mermaid-zoom");
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-label", "Diagram, enlarged");

    var panel = el("div", "mermaid-zoom__panel");
    var inner = el("div", "mermaid-zoom__inner");
    panel.appendChild(inner);

    var bar = el("div", "mermaid-zoom__bar");
    var level = el("span", "mermaid-zoom__level", "100%");
    bar.appendChild(
      button("−", "Zoom out", function () {
        zoom.by(1 / ZOOM_STEP);
      })
    );
    bar.appendChild(level);
    bar.appendChild(
      button("+", "Zoom in", function () {
        zoom.by(ZOOM_STEP);
      })
    );
    bar.appendChild(
      button("Fit", "Fit to window", function () {
        zoom.fit();
      })
    );
    var close = button("×", "Close", closeOverlay);
    close.classList.add("mermaid-zoom__close");
    bar.appendChild(close);

    var hint = el(
      "div",
      "mermaid-zoom__hint",
      "Ctrl+wheel or + / − to zoom, Esc to close"
    );

    overlay.appendChild(bar);
    overlay.appendChild(panel);
    overlay.appendChild(hint);
    overlay.addEventListener("click", function (e) {
      if (e.target === overlay || e.target === panel) closeOverlay();
    });
    panel.addEventListener(
      "wheel",
      function (e) {
        if (!e.ctrlKey || !zoom) return;
        e.preventDefault();
        zoom.by(e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP);
      },
      { passive: false }
    );
    document.body.appendChild(overlay);
    document.body.classList.add("mermaid-zoom-open");
    document.addEventListener("keydown", onKey);
    close.focus();

    counter += 1;
    window.mermaid
      .render("mermaid-zoom-svg-" + counter, source)
      .then(function (out) {
        if (!overlay) return;
        inner.innerHTML = out.svg;
        var svg = inner.querySelector("svg");
        if (!svg) return;
        if (out.bindFunctions) out.bindFunctions(inner);
        // Mermaid pins the width it rendered at; the viewBox scales it.
        svg.style.maxWidth = "";
        svg.style.height = "auto";
        svg.removeAttribute("width");
        svg.removeAttribute("height");
        var vb = (svg.getAttribute("viewBox") || "0 0 1 1").split(/\s+/);
        var aspect = parseFloat(vb[2]) / parseFloat(vb[3]) || 1;
        var scale = 1;
        var fitWidth = 0;

        function apply() {
          svg.style.width = Math.round(fitWidth * scale) + "px";
          level.textContent = Math.round(scale * 100) + "%";
          inner.classList.toggle("mermaid-zoom__inner--scroll", scale > 1);
        }
        zoom = {
          set: function (s) {
            scale = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, s));
            apply();
          },
          by: function (f) {
            zoom.set(scale * f);
          },
          fit: function () {
            var cs = getComputedStyle(inner);
            var pad =
              parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
            var padY =
              parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
            var w = panel.clientWidth - pad;
            var h = panel.clientHeight - padY;
            fitWidth = Math.max(1, Math.min(w, h * aspect));
            zoom.set(1);
          },
        };
        zoom.fit();
      })
      .catch(function () {
        inner.textContent = "The diagram could not be rendered.";
      });
  }

  function onClick(e) {
    if (!window.mermaid || typeof window.mermaid.render !== "function") return;
    var host = e.target.closest(".md-typeset .mermaid");
    if (!host) return;
    var hosts = document.querySelectorAll(".md-typeset .mermaid");
    var index = Array.prototype.indexOf.call(hosts, host);
    if (index < 0) return;
    collectSources().then(function (list) {
      if (list[index]) openOverlay(list[index]);
    });
  }

  // Capture the sources before the theme replaces the <pre> elements, when
  // this runs early enough; otherwise the click handler fetches them.
  collectSources();
  document.addEventListener("click", onClick);
})();
