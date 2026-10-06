package site

import (
	"fmt"
	"math"
	"time"
)

// UsageDay counts completed, credentialed VM reservation intervals, including startup and
// cleanup. It is neither job execution time nor a billing measurement.
type UsageDay struct {
	Date      string  `json:"date"`
	VMSeconds float64 `json:"vm_seconds"`
	VMs       int     `json:"vms"`
}

type UsagePoint struct {
	Date, Label, Minutes string
	VMs, X, Y, Height    int
}
type UsageChart struct {
	Days         []UsagePoint
	TotalMinutes string
	TotalVMs     int
}

func (e Enrollment) UsageChart() UsageChart {
	c := UsageChart{}
	max := 0.0
	total := 0.0
	for _, d := range e.Usage {
		if d.VMSeconds > max {
			max = d.VMSeconds
		}
		total += d.VMSeconds
		c.TotalVMs += d.VMs
	}
	c.TotalMinutes = fmt.Sprintf("%.1f", total/60)
	if max == 0 {
		max = 1
	}
	for i, d := range e.Usage {
		date, _ := time.Parse("2006-01-02", d.Date)
		height := int(math.Round(d.VMSeconds / max * 100))
		c.Days = append(c.Days, UsagePoint{Date: d.Date, Label: date.Format("Jan 2"), Minutes: fmt.Sprintf("%.1f", d.VMSeconds/60), VMs: d.VMs, X: 25 + i*55, Y: 115 - height, Height: height})
	}
	return c
}

func validUsage(days []UsageDay, now time.Time) bool {
	if len(days) > 7 {
		return false
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	previous := ""
	for _, d := range days {
		date, e := time.Parse("2006-01-02", d.Date)
		if e != nil || date.Format("2006-01-02") != d.Date || date.After(today) || date.Before(today.AddDate(0, 0, -6)) || d.Date <= previous || math.IsNaN(d.VMSeconds) || math.IsInf(d.VMSeconds, 0) || d.VMSeconds < 0 || d.VMSeconds > 86400*32 || d.VMs < 0 || d.VMs > 10000 {
			return false
		}
		previous = d.Date
	}
	return true
}
