package ui

import "embed"

// Assets contains the desktop client's interface.
//
//go:embed index.html app.css app.js
var Assets embed.FS
