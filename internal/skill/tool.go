package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/model"
)

const maxToolInputBytes = 4096

// Toolset serves skill bodies from an already collected catalog.
type Toolset struct {
	Skills []Skill
}

func (t *Toolset) Definitions() []model.Tool {
	if len(t.Skills) == 0 {
		return nil
	}
	names := make([]string, len(t.Skills))
	for i, s := range t.Skills {
		names[i] = s.Name
	}
	return []model.Tool{{
		Name:        ToolName,
		Description: ToolDescription,
		Properties:  map[string]any{"name": map[string]any{"type": "string", "enum": names}},
		Required:    []string{"name"},
	}}
}

func (t *Toolset) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if name != ToolName {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	var args struct {
		Name string `json:"name"`
	}
	if len(input) > maxToolInputBytes {
		return "", errors.New("tool input exceeds 4096 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", errors.New("invalid tool arguments")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("tool arguments must contain one JSON object")
	}
	for _, s := range t.Skills {
		if s.Name == args.Name {
			return fmt.Sprintf("Skill %q: %s\n\n%s", s.Name, s.Description, s.Body), nil
		}
	}
	names := make([]string, len(t.Skills))
	for i, s := range t.Skills {
		names[i] = s.Name
	}
	return "", fmt.Errorf("unknown skill %q; available skills: %s", args.Name, strings.Join(names, ", "))
}
