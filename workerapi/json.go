package workerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Reject duplicate object keys and excessive nesting before struct decoding.
// This avoids parser-dependent identity/version interpretations.
func strictJSON(data []byte, target any) error {
	scan := json.NewDecoder(bytes.NewReader(data))
	scan.UseNumber()
	if err := jsonValue(scan, 0); err != nil {
		return err
	}
	if _, err := scan.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return fmt.Errorf("JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate JSON key")
			}
			seen[s] = true
			if err = jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case '[':
		for d.More() {
			if err = jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
}
