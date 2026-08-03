package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// UnmarshalJSONUseNumber decodes exactly one JSON value while preserving numbers as json.Number.
func UnmarshalJSONUseNumber(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}

	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: multiple values")
		}
		return err
	}
	return nil
}
