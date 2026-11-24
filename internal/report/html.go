package report

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"strings"

	"wls-timeline/internal/timeline"
)

//go:embed timeline.html
var pageTemplate string

type payload struct {
	Events         []timeline.Event `json:"events"`
	Source         string           `json:"source"`
	Zone           string           `json:"zone"`
	Warnings       []string         `json:"warnings"`
	TotalLines     int              `json:"totalLines"`
	DetailedEvents *string          `json:"detailedEvents"`
}

// Generate embeds events and optional CSS in an offline HTML report.
func Generate(events []timeline.Event, paths []string, zone string, warnings []string, title string, total int, detailed string, css *string) (string, error) {
	var mode *string
	if detailed != "" {
		mode = &detailed
	}
	data, err := json.Marshal(payload{events, strings.Join(paths, ", "), zone, warnings, total, mode})
	if err != nil {
		return "", err
	}
	style := ""
	if css != nil {
		style = `<link id="custom-stylesheet" rel="stylesheet" href="data:text/css;charset=utf-8;base64,` + base64.StdEncoding.EncodeToString([]byte(*css)) + `">`
	}
	// A single pass prevents log text or titles containing template markers from
	// being processed as another replacement. JSON escapes HTML-sensitive bytes.
	return strings.NewReplacer("__STYLESHEET__", style, "__TITLE__", html.EscapeString(title), "__PAYLOAD__", string(data)).Replace(pageTemplate), nil
}
