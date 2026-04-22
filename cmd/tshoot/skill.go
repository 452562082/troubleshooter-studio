package main

import (
	"fmt"
)

func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tshoot skill new <name> [flags]")
	}
	switch args[0] {
	case "new":
		return runSkillNew(args[1:])
	default:
		return fmt.Errorf("unknown skill subcommand: %s (supported: new)", args[0])
	}
}
