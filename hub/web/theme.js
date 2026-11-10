// Shared by / and /admin. The toggle overrides the system setting and is remembered in this browser.
(function () {
  var root = document.documentElement;
  try {
    var saved = localStorage.getItem("theme");
    if (saved === "light" || saved === "dark") root.dataset.theme = saved;
  } catch (e) { /* private window or blocked storage: the system setting still applies */ }
  var dark = matchMedia("(prefers-color-scheme: dark)");
  function mode() { return root.dataset.theme || (dark.matches ? "dark" : "light"); }
  function paint(btn) {
    var m = mode();
    btn.dataset.mode = m;
    btn.setAttribute("aria-label", m === "dark" ? "Switch to light theme" : "Switch to dark theme");
    btn.title = btn.getAttribute("aria-label");
  }
  document.addEventListener("DOMContentLoaded", function () {
    var btn = document.getElementById("themebtn");
    if (!btn) return;
    paint(btn);
    btn.addEventListener("click", function () {
      var next = mode() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { localStorage.setItem("theme", next); } catch (e) { /* keep the choice for this page only */ }
      paint(btn);
    });
    dark.addEventListener("change", function () { paint(btn); });
  });
})();
