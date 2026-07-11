package logviewer

import "github.com/gur-shatz/go-run/pkg/backoffice/logparser"

func prepareFilter(filter Filter) (Filter, error) {
	return logparser.PrepareFilter(filter)
}

func matchEntry(entry Entry, filter Filter) bool {
	return logparser.MatchEntry(entry, filter)
}
