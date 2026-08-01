// Shared backoffice page glue. Kept deliberately tiny: pages render
// server-side; this only gives htmx fragment failures a visible outcome
// instead of the default silent no-swap.
(function () {
  function markFailed(e) {
    var t = e.detail && e.detail.target;
    if (!t) return;
    var status = e.detail.xhr ? " (" + e.detail.xhr.status + ")" : "";
    t.innerHTML = '<div style="padding:10px 14px"><span class="pill err">load failed' + status + "</span></div>";
  }
  document.body.addEventListener("htmx:responseError", markFailed);
  document.body.addEventListener("htmx:sendError", markFailed);
})();
