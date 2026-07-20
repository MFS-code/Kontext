package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimeapi "github.com/MFS-code/Kontext/runtimes/reference/internal/runtimeapi"
)

func validateToolDefinitions(definitions []runtimeapi.ToolDefinition) error {
	for index, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" ||
			strings.TrimSpace(definition.Description) == "" ||
			!json.Valid(definition.InputSchema) {
			return fmt.Errorf("tool definition %d is invalid", index)
		}
	}
	return nil
}

func validateMessage(index int, message runtimeapi.Message) error {
	if message.Role != runtimeapi.RoleUser &&
		message.Role != runtimeapi.RoleAssistant &&
		message.Role != runtimeapi.RoleTool {
		return fmt.Errorf(
			"message %d has unsupported role %q",
			index,
			message.Role,
		)
	}
	return nil
}
