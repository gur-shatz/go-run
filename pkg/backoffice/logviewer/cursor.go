package logviewer

import (
	"encoding/base64"
	"encoding/json"
)

func encodeCursor(c Cursor) string {
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errInvalidCursor
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return Cursor{}, errInvalidCursor
	}
	return c, nil
}
