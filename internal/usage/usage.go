// Package usage records completed reserved VM time, never job secrets or billing.
package usage

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Record struct {
	ID        string    `json:"id"`
	Scope     string    `json:"github_url"`
	Label     string    `json:"label"`
	Reserved  time.Time `json:"reserved_at"`
	Completed time.Time `json:"completed_at"`
}
type Day struct {
	Date      string  `json:"date"`
	VMSeconds float64 `json:"vm_seconds"`
	VMs       int     `json:"vms"`
}

func Load(dir string) ([]Record, error) {
	file, err := os.Open(filepath.Join(dir, "usage.json"))
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return nil, errors.New("usage store exceeds limit")
	}
	var records []Record
	if json.Unmarshal(b, &records) != nil || len(records) > 10000 {
		return nil, errors.New("invalid usage store")
	}
	return records, nil
}
func Append(dir string, record Record) error {
	if record.ID == "" || record.Scope == "" || record.Label == "" || record.Reserved.IsZero() || record.Completed.Before(record.Reserved) || record.Completed.Sub(record.Reserved) > 25*time.Hour {
		return errors.New("invalid usage interval")
	}
	records, err := Load(dir)
	if err != nil {
		return err
	}
	kept := []Record{}
	cutoff := record.Completed.AddDate(0, 0, -30)
	for _, old := range records {
		if old.ID == record.ID {
			return nil
		}
		if !old.Completed.Before(cutoff) {
			kept = append(kept, old)
		}
	}
	if len(kept) >= 10000 {
		return errors.New("usage store capacity reached")
	}
	kept = append(kept, record)
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".usage-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(b); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(dir, "usage.json")); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
func Aggregate(records []Record, scope string, now time.Time) []Day {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	days := make([]Day, 7)
	for i := range days {
		days[i].Date = today.AddDate(0, 0, i-6).Format("2006-01-02")
	}
	for _, record := range records {
		if strings.ToLower(strings.TrimRight(record.Scope, "/")) != strings.ToLower(strings.TrimRight(scope, "/")) {
			continue
		}
		for i := range days {
			start := today.AddDate(0, 0, i-6)
			end := start.AddDate(0, 0, 1)
			lower := record.Reserved
			if lower.Before(start) {
				lower = start
			}
			upper := record.Completed
			if upper.After(end) {
				upper = end
			}
			if upper.After(lower) {
				days[i].VMSeconds += upper.Sub(lower).Seconds()
			}
			if !record.Completed.Before(start) && record.Completed.Before(end) {
				days[i].VMs++
			}
		}
	}
	return days
}
