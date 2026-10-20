package plan

import (
	"os"
)

// ReadFile decodes a JSON plan from a file.
func ReadFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
