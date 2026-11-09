// reset-password changes an existing SQLite installation's administrator password.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("db", "", "existing SQLite database file (default: configured path)")
	flag.Parse()
	if *path == "" {
		resolved, _, err := db.DSN()
		if err != nil {
			return err
		}
		*path = resolved
	}
	if _, err := os.Stat(*path); err != nil {
		return fmt.Errorf("database must already exist: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Read new password from stdin (8+ characters, max 72 bytes):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return fmt.Errorf("password must contain at least 8 characters and at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	store, err := db.Open(*path)
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.SetSetting("auth.password_hash", string(hash)); err != nil {
		return err
	}
	fmt.Println("Password updated.")
	return nil
}
