package cli

import "encoding/json"

func fmtAny(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
