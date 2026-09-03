package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const keyringService = "checkmk-fzf"

var errSecretNotFound = keyring.ErrNotFound

type secretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (systemKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (systemKeyring) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

func credentialAccount(cfg config) string {
	return fmt.Sprintf("%s@%s/%s", cfg.Username, cfg.URL, cfg.Site)
}

func newAuthCommand(stdin io.Reader, stdout, stderr io.Writer, secrets secretStore) *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage the Checkmk credential in the system keyring",
	}
	auth.AddCommand(
		&cobra.Command{
			Use:   "login",
			Short: "Save the Checkmk automation secret in the system keyring",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				fmt.Fprintf(stderr, "Checkmk secret for %s: ", cfg.Username)
				secret, err := readSecret(stdin)
				fmt.Fprintln(stderr)
				if err != nil {
					return err
				}
				if secret == "" {
					return errors.New("secret cannot be empty")
				}
				if err := secrets.Set(keyringService, credentialAccount(cfg), secret); err != nil {
					return fmt.Errorf("save credential in keyring: %w", err)
				}
				fmt.Fprintf(stdout, "Credential saved for %s.\n", cfg.Username)
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Check whether a credential is stored",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if _, err := secrets.Get(keyringService, credentialAccount(cfg)); err != nil {
					if errors.Is(err, errSecretNotFound) {
						return errors.New("no Checkmk credential is stored; run checkmk-fzf auth login")
					}
					return fmt.Errorf("read credential from keyring: %w", err)
				}
				fmt.Fprintf(stdout, "Credential is stored for %s.\n", cfg.Username)
				return nil
			},
		},
		&cobra.Command{
			Use:   "logout",
			Short: "Remove the Checkmk credential from the system keyring",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if err := secrets.Delete(keyringService, credentialAccount(cfg)); err != nil && !errors.Is(err, errSecretNotFound) {
					return fmt.Errorf("delete credential from keyring: %w", err)
				}
				fmt.Fprintf(stdout, "Credential removed for %s.\n", cfg.Username)
				return nil
			},
		},
	)
	return auth
}

func readSecret(reader io.Reader) (string, error) {
	if file, ok := reader.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		return string(value), nil
	}
	value, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r"), nil
}
