package main

import (
	"errors"
	"fmt"
)

func runMigrateCommand(args []string) error {
	if len(args) == 0 || args[0] == "sqlite-to-postgres" {
		return errors.New(`the sqlite-to-postgres migration command has been removed; Kyvik now supports postgres-only storage`)
	}
	return fmt.Errorf("unknown migrate subcommand: %s", args[0])
}
