//go:build !linux

package main

import "fmt"

func runTokenMigration(old, output, key, url, enroll string, dev, token bool) error {
	return fmt.Errorf("token migration is supported only on Linux")
}
