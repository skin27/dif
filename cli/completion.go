package cli

import (
	"dif/api"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// completionSuggestions explores commands in DIF's interactive shell.
func completionSuggestions(line string) ([]string, error) {
	args, err := utilityArguments(line)
	if err != nil {
		return nil, err
	}
	if len(line) > 0 && unicode.IsSpace(rune(line[len(line)-1])) {
		args = append(args, "")
	}
	if len(args) == 0 {
		args = []string{""}
	}
	prefix := args[len(args)-1]
	var choices []string
	if len(args) == 1 {
		for _, c := range commands {
			choices = append(choices, c.name)
		}
	} else {
		previous := args[len(args)-2]
		switch {
		case previous == "--output":
			choices = []string{"text", "json"}
		case previous == "--template":
			choices = []string{"hello", "timer", "file", "http"}
		case args[0] == "help":
			for _, c := range commands {
				choices = append(choices, c.name)
			}
		case strings.HasPrefix(prefix, "-"):
			choices = []string{"--help"}
			switch args[0] {
			case "init":
				choices = append(choices, "--template", "--id")
			case "validate", "describe", "catalog", "version":
				choices = append(choices, "--output")
			case "stop":
				choices = append(choices, "--force")
			case "log":
				choices = append(choices, "--lines")
			}
		case args[0] == "catalog":
			for _, s := range api.StepCatalog() {
				choices = append(choices, s.Name)
			}
		case args[0] == "list" || args[0] == "ps":
			choices = []string{"started", "paused", "stopped"}
		case args[0] == "init" || args[0] == "validate" || args[0] == "describe" || args[0] == "run" || args[0] == "load":
			dir := filepath.Dir(prefix)
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				candidate := prefix[:strings.LastIndexAny(prefix, `/\`)+1] + entry.Name()
				if entry.IsDir() {
					candidate += string(filepath.Separator)
				}
				choices = append(choices, candidate)
			}
		}
	}
	result := []string{}
	seen := map[string]bool{}
	for _, choice := range choices {
		if strings.HasPrefix(choice, prefix) && !seen[choice] {
			result = append(result, choice)
			seen[choice] = true
		}
	}
	sort.Strings(result)
	return result, nil
}
