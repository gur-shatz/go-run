// Shared backoffice page glue. Currently empty, and served anyway: it is
// the seam where behavior common to every backoffice page belongs, and
// pages reference it relatively at any mount depth.
//
// It previously gave htmx swap failures a visible outcome. The page that
// used htmx now binds to a JSON document instead and reports a failed
// fetch itself, next to the markup that would have shown the data, so
// there is no longer a shared failure mode to handle here.
