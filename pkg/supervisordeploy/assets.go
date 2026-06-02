package supervisordeploy

import "embed"

// Assets contains the generic supervisor deployment chart.
//
//go:embed chart/**
var Assets embed.FS
