// Package config handles tocy's configuration file (~/.tocy/config.yaml).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Budget struct {
	Daily  float64 `yaml:"daily"`
	Weekly float64 `yaml:"weekly"`
	Monthly float64 `yaml:"monthly"`
}

type Config struct {
	DefaultRange string `yaml:"default_range"`
	DefaultGroup string `yaml:"default_group"`
	Budget       Budget `yaml:"budget"`
	Notifications bool  `yaml:"notifications"`
	AlertThreshold float64 `yaml:"alert_threshold"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tocy", "config.yaml")
}

func Load(path string) *Config {
	c := &Config{
		DefaultRange: "7d",
		DefaultGroup: "tool",
		Budget: Budget{
			Daily:   5.0,
			Weekly:  25.0,
			Monthly: 100.0,
		},
		AlertThreshold: 0.8,
	}
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c
		}
		return c
	}
	// Simple key-value parsing without YAML dependency
	for _, line := range splitLines(data) {
		line = trimLine(line)
		if line == "" || line[0] == '#' {
			continue
		}
		parts := splitKV(line)
		if len(parts) < 2 {
			continue
		}
		key := parts[0]
		val := parts[1]
		switch key {
		case "default_range":
			c.DefaultRange = val
		case "default_group":
			c.DefaultGroup = val
		case "daily_budget":
			c.Budget.Daily = parseFloat64(val, 5.0)
		case "weekly_budget":
			c.Budget.Weekly = parseFloat64(val, 25.0)
		case "monthly_budget":
			c.Budget.Monthly = parseFloat64(val, 100.0)
		case "alert_threshold":
			c.AlertThreshold = parseFloat64(val, 0.8)
		case "notifications":
			c.Notifications = val == "true" || val == "1"
		}
	}
	return c
}

func (c *Config) CheckBudget(cost float64, since time.Time) (string, bool) {
	switch {
	case isToday(since):
		if cost > c.Budget.Daily {
			return fmt.Sprintf("%.0f%%", cost/c.Budget.Daily*100), true
		}
	case isWeek(since):
		if cost > c.Budget.Weekly {
			return fmt.Sprintf("%.0f%%", cost/c.Budget.Weekly*100), true
		}
	default:
		if cost > c.Budget.Monthly {
			return fmt.Sprintf("%.0f%%", cost/c.Budget.Monthly*100), true
		}
	}
	return "", false
}

func (c *Config) IsOverThreshold(cost float64, since time.Time) bool {
	_, exceeded := c.CheckBudget(cost, since)
	return exceeded
}

func isToday(t time.Time) bool {
	now := time.Now()
	y, m, d := t.Date()
	ny, nm, nd := now.Date()
	return y == ny && m == nm && d == nd
}

func isWeek(t time.Time) bool {
	return t.Before(time.Now().AddDate(0, 0, -7))
}

func splitLines(data []byte) []string {
	var lines []string
	var current []byte
	for _, b := range data {
		if b == '\n' {
			lines = append(lines, string(current))
			current = nil
		} else {
			current = append(current, b)
		}
	}
	if len(current) > 0 {
		lines = append(lines, string(current))
	}
	return lines
}

func trimLine(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func splitKV(s string) []string {
	parts := []string{}
	var current []byte
	inQuote := false
	for _, b := range s {
		switch {
		case b == '=' && !inQuote:
			parts = append(parts, string(current))
			current = nil
		case b == '"':
			inQuote = !inQuote
		default:
			current = append(current, byte(b))
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}
	return parts
}

func parseFloat64(s string, def float64) float64 {
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}
